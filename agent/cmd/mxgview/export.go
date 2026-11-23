package main

import (
	"archive/zip"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"mxg/internal/must"
)

// "Export for AI": one zip with the context guide, daily summary, events, aggregated inverter/BMS series and the
// inverter settings — compact enough to upload to a chat (10-min step ≈ 5k tokens/day, 1 h ≈ 1k tokens/day).

// columns that get _min/_max in the aggregates
var invStats = map[string]bool{"grid_v": true, "out_v": true, "batt_v": true, "load_w": true, "batt_w": true,
	"temp_a": true, "temp_b": true, "temp_dc": true}
var bmsStats = map[string]bool{"soc": true, "voltage": true, "current": true, "power": true, "cell_min": true,
	"cell_max": true, "cell_delta": true, "temp1": true, "temp2": true, "temp_mos": true}

// export drops columns that carry nothing here (no PV panels; internal indices)
var invExportCols = []string{"work_state", "grid_v", "out_v", "out_hz", "batt_v", "soc_est", "load_w", "load_pct",
	"inv_w", "grid_w", "bus_v", "temp_a", "temp_b", "temp_dc", "temp_chg", "batt_w", "batt_a",
	"err_inv", "warn_inv", "err_chg", "warn_chg", "e_load", "e_buy", "e_discharge", "e_grid_charge", "e_charge"}
var bmsExportCols = []string{"soc", "voltage", "current", "power", "remaining_ah", "cell_min", "cell_max",
	"cell_delta", "cell_min_idx", "cell_max_idx", "temp1", "temp2", "temp_mos", "balancing", "balance_a",
	"charge_on", "discharge_on", "errors", "cycles", "soh", "c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8"}

func fmtNum(x float64) string { return fmtDec(x, 3) }

func fmtDec(x float64, dec int) string {
	p := math.Pow(10, float64(dec))
	x = math.Round(x*p) / p
	if x == math.Trunc(x) && math.Abs(x) < 1e15 {
		return strconv.FormatInt(int64(x), 10)
	}
	return strconv.FormatFloat(x, 'f', -1, 64)
}

// decimals per column in the aggregates: cell voltages need mV, the rest is fine with 2 (saves tokens)
func decOf(col string) int {
	switch col {
	case "cell_min", "cell_max", "cell_delta", "c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "balance_a":
		return 3
	}
	return 2
}

// exportAgg overrides aggKind where an average makes no sense
func exportAgg(col string) string {
	switch col {
	case "cell_min_idx", "cell_max_idx", "work_state":
		return "last"
	case "balancing":
		return "max"
	}
	return aggKind(col)
}

// aggregateCSV writes bucketed rows: time, n, then per column the aggregate (avg / OR / last / min / max by
// aggKind) and for stats columns also _min and _max.
func aggregateCSV(w io.Writer, rows []row, step int, cols []string, stats map[string]bool) error {
	cw := csv.NewWriter(w)
	hdr := []string{"time", "n"}
	for _, c := range cols {
		hdr = append(hdr, c)
		if stats[c] {
			hdr = append(hdr, c+"_min", c+"_max")
		}
	}
	cw.Write(hdr)
	type acc struct {
		sum, min, max, last float64
		or                  uint64
		n                   int
	}
	var cur int64 = -1
	n := 0
	accs := map[string]*acc{}
	flush := func() {
		if cur < 0 || n == 0 {
			return
		}
		rec := []string{time.Unix(cur, 0).Format("2006-01-02 15:04"), strconv.Itoa(n)}
		for _, c := range cols {
			a := accs[c]
			if a == nil || a.n == 0 {
				rec = append(rec, "")
				if stats[c] {
					rec = append(rec, "", "")
				}
				continue
			}
			var x float64
			switch exportAgg(c) {
			case "or":
				x = float64(a.or)
			case "last":
				x = a.last
			case "min":
				x = a.min
			case "max":
				x = a.max
			default:
				x = a.sum / float64(a.n)
			}
			d := decOf(c)
			rec = append(rec, fmtDec(x, d))
			if stats[c] {
				rec = append(rec, fmtDec(a.min, d), fmtDec(a.max, d))
			}
		}
		cw.Write(rec)
	}
	for _, r := range rows {
		b := r.t - r.t%int64(step)
		if b != cur {
			flush()
			cur, n, accs = b, 0, map[string]*acc{}
		}
		n++
		for _, c := range cols {
			v, ok := r.vals[c]
			if !ok {
				continue
			}
			a := accs[c]
			if a == nil {
				a = &acc{min: v, max: v}
				accs[c] = a
			}
			a.sum += v
			a.last = v
			a.or |= uint64(v)
			a.min = math.Min(a.min, v)
			a.max = math.Max(a.max, v)
			a.n++
		}
	}
	flush()
	cw.Flush()
	return cw.Error()
}

func guideText() string {
	exe, _ := os.Executable()
	for _, p := range []string{
		filepath.Join(filepath.Dir(exe), "..", "..", "docs", "ANALYSIS_GUIDE.md"),
		filepath.Join(*dataDir, "..", "docs", "ANALYSIS_GUIDE.md"),
	} {
		if b, err := os.ReadFile(p); err == nil {
			return string(b)
		}
	}
	return "(docs/ANALYSIS_GUIDE.md не найден рядом с просмотрщиком)\n"
}

func ptr(p *float64) string {
	if p == nil {
		return ""
	}
	return fmtNum(*p)
}

func writeExport(w http.ResponseWriter, from, to string, step int) {
	name := fmt.Sprintf("mxg_ai_%s_%s_%dmin.zip", from, to, step/60)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	zw := zip.NewWriter(w)
	defer zw.Close()
	now := time.Now()
	z := zipFiles{zw, now}

	f, _ := z.Create("README_FIRST.md")
	fmt.Fprintf(f, "Экспорт данных mxg: период %s — %s, шаг агрегатов %d мин, сформирован %s.\n"+
		"Файлы: daily_summary.csv, events.csv, inverter_%dmin.csv, bms_%dmin.csv, settings.json.\n\n",
		from, to, step/60, time.Now().Format("2006-01-02 15:04"), step/60, step/60)
	io.WriteString(f, guideText())

	// daily summary
	f, _ = z.Create("daily_summary.csv")
	cw := csv.NewWriter(f)
	cw.Write([]string{"day", "rows", "outage_min", "outages", "soc_min", "soc_max", "ah_discharged", "ah_charged",
		"cell_delta_max_v", "batt_v_min", "batt_v_max", "load_w_avg", "load_w_max", "temp_max",
		"e_load_kwh", "e_buy_kwh", "e_discharge_kwh", "e_grid_charge_kwh", "fault_rows"})
	for _, d := range days() {
		if d < from || d > to {
			continue
		}
		s := summarize(d)
		bmsSummary(&s)
		cw.Write([]string{s.Day, strconv.Itoa(s.Rows), fmtNum(s.OutageMin), strconv.Itoa(s.Outages), ptr(s.SocMin), ptr(s.SocMax),
			ptr(s.AhOut), ptr(s.AhIn), ptr(s.DeltaMax), ptr(s.BattMin), ptr(s.BattMax), ptr(s.LoadAvg), ptr(s.LoadMax),
			ptr(s.TempMax), ptr(s.ELoad), ptr(s.EBuy), ptr(s.EDischarge), ptr(s.EGridCharge), strconv.Itoa(s.Faults)})
	}
	cw.Flush()

	// events
	f, _ = z.Create("events.csv")
	cw = csv.NewWriter(f)
	cw.Write([]string{"time", "kind", "notified", "text"})
	for _, e := range readEvents(from, to) {
		cw.Write([]string{e.Time, e.Kind, strconv.FormatBool(e.Notified), e.Text})
	}
	cw.Flush()

	// aggregates
	f, _ = z.Create(fmt.Sprintf("inverter_%dmin.csv", step/60))
	aggregateCSV(f, readRangeP("data", from, to), step, invExportCols, invStats)
	f, _ = z.Create(fmt.Sprintf("bms_%dmin.csv", step/60))
	aggregateCSV(f, readRangeP("bms", from, to), step, bmsExportCols, bmsStats)

	// settings
	if st, err := loadSettings(); err == nil {
		type item struct {
			Group string  `json:"group"`
			Name  string  `json:"name"`
			Value float64 `json:"value"`
			Text  string  `json:"text"`
		}
		var items []item
		for _, s := range must.Settings {
			if v, ok := st.Values[s.Key]; ok {
				items = append(items, item{s.Group, s.Name, v, s.Format(v)})
			}
		}
		f, _ = z.Create("settings.json")
		b, _ := json.MarshalIndent(map[string]any{"read_at": st.Time, "inverter_settings": items}, "", " ")
		f.Write(b)
	}
}

// zipFiles creates entries with a real modification time (zip.Create leaves 1979).
type zipFiles struct {
	w   *zip.Writer
	mod time.Time
}

func (z zipFiles) Create(name string) (io.Writer, error) {
	return z.w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: z.mod})
}

func registerExportAPI(mux *http.ServeMux) {
	mux.HandleFunc("/api/export", func(w http.ResponseWriter, r *http.Request) {
		from, to := rangeParams(r)
		step, _ := strconv.Atoi(r.URL.Query().Get("step"))
		if step != 600 && step != 1800 && step != 3600 && step != 60 {
			step = 600
		}
		writeExport(w, from, to, step)
	})
}
