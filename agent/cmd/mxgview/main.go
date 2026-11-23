// mxgview is the PC-side viewer: it copies the agent's CSV files from the box over adb and serves a local page
// (http://localhost:8787) with charts, a table, daily summaries and the event log. Nothing leaves the PC.
//
//	mxgview [-box IP:5555] [-data DIR] [-port 8787] [-nosync] [-nobrowser]
//
// The box address comes from -box or the MXG_BOX environment variable (e.g. set in view.bat).
package main

import (
	"bufio"
	"context"
	"embed"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

var (
	box      = flag.String("box", os.Getenv("MXG_BOX"), "adb address of the box, IP:5555 (default: $MXG_BOX)")
	dataDir  = flag.String("data", "", "local data folder (default: data/ in the project root, two levels above the exe)")
	port     = flag.Int("port", 8787, "local http port")
	noSync   = flag.Bool("nosync", false, "do not pull from the box")
	noBrowse = flag.Bool("nobrowser", false, "do not open the browser")
)

// ---------- sync over adb ----------

type syncState struct {
	mu      sync.Mutex
	running bool
	last    time.Time
	lastOK  time.Time
	msg     string
}

var st syncState

var remoteFile = regexp.MustCompile(`^((data|bms)-\d{4}-\d{2}-\d{2}(\.v\d+)?\.csv|events\.csv|settings\.json)$`)

func adb(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "adb", args...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// syncOnce pulls changed files (size differs) from /data/mxg.
func syncOnce() {
	st.mu.Lock()
	if st.running {
		st.mu.Unlock()
		return
	}
	st.running = true
	st.mu.Unlock()
	msg, ok := doSync()
	st.mu.Lock()
	st.running, st.last, st.msg = false, time.Now(), msg
	if ok {
		st.lastOK = st.last
	}
	st.mu.Unlock()
	log.Printf("sync: %s", msg)
}

func doSync() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	devs, _ := adb(ctx, "devices")
	if !strings.Contains(devs, *box+"\tdevice") {
		adb(ctx, "connect", *box)
	}
	out, err := adb(ctx, "-s", *box, "shell", "ls -l /data/mxg/")
	if err != nil || strings.Contains(out, "error") {
		return "приставка недоступна: " + strings.TrimSpace(firstLineOf(out)), false
	}
	pulled, failed := 0, 0
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 6 {
			continue
		}
		name := f[len(f)-1]
		if !remoteFile.MatchString(name) {
			continue
		}
		size, err := strconv.ParseInt(f[3], 10, 64) // toolbox ls -l: perms owner group size date time name
		if err != nil {
			continue
		}
		local := filepath.Join(*dataDir, name)
		if fi, err := os.Stat(local); err == nil && fi.Size() == size {
			continue
		}
		tmp := local + ".part"
		if o, err := adb(ctx, "-s", *box, "pull", "/data/mxg/"+name, tmp); err != nil {
			failed++
			log.Printf("pull %s: %v %s", name, err, firstLineOf(o))
			os.Remove(tmp)
			continue
		}
		if err := os.Rename(tmp, local); err != nil {
			os.Remove(local)
			os.Rename(tmp, local)
		}
		pulled++
	}
	if failed > 0 {
		return fmt.Sprintf("обновлено файлов: %d, ошибок: %d", pulled, failed), false
	}
	return fmt.Sprintf("обновлено файлов: %d", pulled), true
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ---------- reading CSV ----------

type row struct {
	t    int64
	vals map[string]float64
}

var dayFile = regexp.MustCompile(`^data-(\d{4}-\d{2}-\d{2})(\.v\d+)?\.csv$`)

// days returns available days (sorted).
func days() []string {
	ents, _ := os.ReadDir(*dataDir)
	set := map[string]bool{}
	for _, e := range ents {
		if m := dayFile.FindStringSubmatch(e.Name()); m != nil {
			set[m[1]] = true
		}
	}
	var out []string
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func readDay(day string) []row { return readDayP("data", day) }

var bmsFile = regexp.MustCompile(`^bms-(\d{4}-\d{2}-\d{2})(\.v\d+)?\.csv$`)

// readDayP reads PREFIX-DAY*.csv (data or bms), columns by header name.
func readDayP(prefix, day string) []row {
	files, _ := filepath.Glob(filepath.Join(*dataDir, prefix+"-"+day+"*.csv"))
	re := dayFile
	if prefix == "bms" {
		re = bmsFile
	}
	var rows []row
	for _, f := range files {
		if !re.MatchString(filepath.Base(f)) {
			continue
		}
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		r := csv.NewReader(bufio.NewReader(fh))
		r.FieldsPerRecord = -1
		hdr, err := r.Read()
		if err != nil {
			fh.Close()
			continue
		}
		for {
			rec, err := r.Read()
			if err != nil {
				break
			}
			if len(rec) < 2 {
				continue
			}
			t, err := time.ParseInLocation("2006-01-02 15:04:05", rec[0], time.Local)
			if err != nil {
				continue
			}
			v := make(map[string]float64, len(hdr))
			for i := 1; i < len(hdr) && i < len(rec); i++ {
				if x, err := strconv.ParseFloat(rec[i], 64); err == nil {
					v[hdr[i]] = x
				}
			}
			rows = append(rows, row{t.Unix(), v})
		}
		fh.Close()
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].t < rows[j].t })
	return rows
}

func readRange(from, to string) []row { return readRangeP("data", from, to) }

func readRangeP(prefix, from, to string) []row {
	var rows []row
	for _, d := range days() {
		if d >= from && d <= to {
			rows = append(rows, readDayP(prefix, d)...)
		}
	}
	return rows
}

var bmsColumns = []string{"soc", "voltage", "current", "power", "remaining_ah", "capacity_ah", "cell_min", "cell_max",
	"cell_delta", "cell_avg", "cell_min_idx", "cell_max_idx", "temp_mos", "temp1", "temp2", "balancing", "balance_a",
	"errors", "charge_on", "discharge_on", "cycles", "soh", "c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8"}

// column aggregation inside a bucket
func aggKind(col string) string {
	switch {
	case strings.HasPrefix(col, "err_") || strings.HasPrefix(col, "warn_") || col == "errors":
		return "or"
	case strings.HasPrefix(col, "e_") || col == "cycles" || col == "soh" || col == "capacity_ah":
		return "last"
	case col == "grid_v" || col == "cell_min" || col == "charge_on" || col == "discharge_on":
		return "min" // keeps short outages / dips / switch-offs visible
	case col == "cell_max" || col == "cell_delta":
		return "max"
	default:
		return "avg"
	}
}

var columns = []string{"work_state", "grid_v", "out_v", "out_hz", "batt_v", "soc_est", "load_w", "load_pct", "inv_w",
	"grid_w", "pv_v", "pv_w", "chg_a", "bus_v", "temp_a", "temp_b", "temp_chg", "temp_dc", "batt_w", "batt_a",
	"err_inv", "warn_inv", "err_chg", "warn_chg", "e_load", "e_buy", "e_pv", "e_discharge", "e_grid_charge", "e_charge"}

type series struct {
	Step int                     `json:"step"`
	T    []int64                 `json:"t"`
	Cols map[string][]*float64   `json:"cols"`
}

func bucketize(rows []row, step int) series { return bucketizeCols(rows, step, columns) }

func bucketizeCols(rows []row, step int, columns []string) series {
	s := series{Step: step, Cols: map[string][]*float64{}}
	if step <= 10 {
		for _, c := range columns {
			s.Cols[c] = make([]*float64, 0, len(rows))
		}
		for _, r := range rows {
			s.T = append(s.T, r.t)
			for _, c := range columns {
				if v, ok := r.vals[c]; ok {
					x := v
					s.Cols[c] = append(s.Cols[c], &x)
				} else {
					s.Cols[c] = append(s.Cols[c], nil)
				}
			}
		}
		return s
	}
	type acc struct {
		sum, min, max, last float64
		or                  uint64
		n                   int
	}
	var cur int64 = -1
	accs := map[string]*acc{}
	flush := func() {
		if cur < 0 {
			return
		}
		s.T = append(s.T, cur)
		for _, c := range columns {
			a := accs[c]
			if a == nil || a.n == 0 {
				s.Cols[c] = append(s.Cols[c], nil)
				continue
			}
			var x float64
			switch aggKind(c) {
			case "or":
				x = float64(a.or)
			case "last":
				x = a.last
			case "min":
				x = a.min
			case "max":
				x = a.max
			default:
				x = math.Round(a.sum/float64(a.n)*100) / 100
			}
			s.Cols[c] = append(s.Cols[c], &x)
		}
		accs = map[string]*acc{}
	}
	for _, r := range rows {
		b := r.t - r.t%int64(step)
		if b != cur {
			flush()
			cur = b
		}
		for c, v := range r.vals {
			a := accs[c]
			if a == nil {
				a = &acc{min: v, max: v}
				accs[c] = a
			}
			a.sum += v
			a.last = v
			a.or |= uint64(v)
			if v < a.min {
				a.min = v
			}
			if v > a.max {
				a.max = v
			}
			a.n++
		}
	}
	flush()
	return s
}

func autoStep(span int64, want int) int {
	for _, s := range []int{10, 30, 60, 120, 300, 600, 1800, 3600, 10800, 21600, 86400} {
		if span/int64(s) <= int64(want) {
			return s
		}
	}
	return 86400
}

// ---------- daily summary ----------

type daySum struct {
	Day         string   `json:"day"`
	Rows        int      `json:"rows"`
	OutageMin   float64  `json:"outage_min"`
	Outages     int      `json:"outages"`
	BattMin     *float64 `json:"batt_min"`
	BattMax     *float64 `json:"batt_max"`
	LoadMax     *float64 `json:"load_max"`
	LoadAvg     *float64 `json:"load_avg"`
	PvMax       *float64 `json:"pv_max"`
	TempMax     *float64 `json:"temp_max"`
	ELoad       *float64 `json:"e_load"`
	EBuy        *float64 `json:"e_buy"`
	EPv         *float64 `json:"e_pv"`
	EDischarge  *float64 `json:"e_discharge"`
	EGridCharge *float64 `json:"e_grid_charge"`
	Faults      int      `json:"faults"`
	SocMin      *float64 `json:"soc_min"`
	SocMax      *float64 `json:"soc_max"`
	DeltaMax    *float64 `json:"delta_max"` // V
	AhOut       *float64 `json:"ah_out"`    // discharged Ah (BMS current integral)
	AhIn        *float64 `json:"ah_in"`
}

// bmsSummary adds BMS-based figures for one day.
func bmsSummary(d *daySum) {
	rows := readDayP("bms", d.Day)
	var smin, smax, dmax, ain, aout float64
	var prevT int64
	for i, r := range rows {
		if v, ok := r.vals["soc"]; ok {
			if d.SocMin == nil || v < smin {
				smin = v
				d.SocMin = &smin
			}
			if d.SocMax == nil || v > smax {
				smax = v
				d.SocMax = &smax
			}
		}
		if v, ok := r.vals["cell_delta"]; ok && (d.DeltaMax == nil || v > dmax) {
			dmax = v
			d.DeltaMax = &dmax
		}
		if i > 0 {
			gap := float64(r.t - prevT)
			if gap > 0 && gap <= 120 {
				if a, ok := r.vals["current"]; ok {
					if a > 0 {
						ain += a * gap / 3600
					} else {
						aout += -a * gap / 3600
					}
				}
			}
		}
		prevT = r.t
	}
	if len(rows) > 1 {
		d.AhIn, d.AhOut = fp(ain), fp(aout)
	}
}

func fp(x float64) *float64 { x = math.Round(x*100) / 100; return &x }

func summarize(day string) daySum {
	rows := readDay(day)
	d := daySum{Day: day, Rows: len(rows)}
	var bmin, bmax, lmax, lsum, pvmax, tmax float64
	var ln int
	first := map[string]float64{}
	last := map[string]float64{}
	inOutage := false
	var prevT int64
	for i, r := range rows {
		gap := int64(10)
		if i > 0 {
			gap = r.t - prevT
			if gap > 120 {
				gap = 10 // missing data is not counted as outage
			}
		}
		prevT = r.t
		if g, ok := r.vals["grid_v"]; ok {
			if g < 150 {
				d.OutageMin += float64(gap) / 60
				if !inOutage {
					d.Outages++
				}
				inOutage = true
			} else {
				inOutage = false
			}
		}
		if v, ok := r.vals["batt_v"]; ok {
			if d.BattMin == nil || v < bmin {
				bmin = v
				d.BattMin = &bmin
			}
			if d.BattMax == nil || v > bmax {
				bmax = v
				d.BattMax = &bmax
			}
		}
		if v, ok := r.vals["load_w"]; ok {
			lsum += v
			ln++
			if d.LoadMax == nil || v > lmax {
				lmax = v
				d.LoadMax = &lmax
			}
		}
		if v, ok := r.vals["pv_w"]; ok && (d.PvMax == nil || v > pvmax) {
			pvmax = v
			d.PvMax = &pvmax
		}
		for _, c := range []string{"temp_a", "temp_b", "temp_dc", "temp_chg"} {
			if v, ok := r.vals[c]; ok && (d.TempMax == nil || v > tmax) {
				tmax = v
				d.TempMax = &tmax
			}
		}
		if v, ok := r.vals["err_inv"]; ok && v != 0 {
			d.Faults++
		}
		for _, c := range []string{"e_load", "e_buy", "e_pv", "e_discharge", "e_grid_charge"} {
			if v, ok := r.vals[c]; ok && v > 0 {
				if _, ok := first[c]; !ok {
					first[c] = v
				}
				last[c] = v
			}
		}
	}
	if ln > 0 {
		d.LoadAvg = fp(lsum / float64(ln))
	}
	d.OutageMin = math.Round(d.OutageMin)
	diff := func(c string) *float64 {
		f, ok1 := first[c]
		l, ok2 := last[c]
		if !ok1 || !ok2 || l < f {
			return nil
		}
		return fp(l - f)
	}
	d.ELoad, d.EBuy, d.EPv, d.EDischarge, d.EGridCharge =
		diff("e_load"), diff("e_buy"), diff("e_pv"), diff("e_discharge"), diff("e_grid_charge")
	return d
}

// ---------- events ----------

type event struct {
	Time     string `json:"time"`
	Kind     string `json:"kind"`
	Notified bool   `json:"notified"`
	Text     string `json:"text"`
}

func readEvents(from, to string) []event {
	fh, err := os.Open(filepath.Join(*dataDir, "events.csv"))
	if err != nil {
		return nil
	}
	defer fh.Close()
	r := csv.NewReader(fh)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.Read() // header
	var out []event
	for {
		rec, err := r.Read()
		if err != nil {
			break
		}
		if len(rec) < 4 || len(rec[0]) < 10 {
			continue
		}
		d := rec[0][:10]
		if d < from || d > to {
			continue
		}
		out = append(out, event{rec[0], rec[1], rec[2] == "true", rec[3]})
	}
	return out
}

// ---------- http ----------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

// guard protects the local server from other web pages open in the browser: only Host 127.0.0.1/localhost
// (DNS rebinding), and POSTs must carry X-Mxg: 1, which a cross-site page cannot send without a CORS preflight
// that this server never approves.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "127.0.0.1" && host != "localhost" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Mxg") != "1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func rangeParams(r *http.Request) (string, string) {
	today := time.Now().Format("2006-01-02")
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if from == "" {
		from = today
	}
	if to == "" {
		to = today
	}
	return from, to
}

func main() {
	flag.Parse()
	if *dataDir == "" {
		exe, _ := os.Executable()
		*dataDir = filepath.Clean(filepath.Join(filepath.Dir(exe), "..", "..", "data"))
	}
	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		log.Fatal(err)
	}
	if *box == "" && !*noSync {
		fmt.Fprintln(os.Stderr, "box address not set: use -box IP:5555 or set MXG_BOX (see view.bat)")
		*noSync = true
	}
	logPath := filepath.Join(*dataDir, "mxgview.log")
	if fi, err := os.Stat(logPath); err == nil && fi.Size() > 1<<20 {
		os.Rename(logPath, logPath+".1")
	}
	if lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, lf))
	}
	log.Printf("mxgview start, data folder: %s", *dataDir)

	if !*noSync {
		go func() {
			for {
				syncOnce()
				time.Sleep(2 * time.Minute)
			}
		}()
	}

	web, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(web)))
	mux.HandleFunc("/api/days", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, days()) })
	mux.HandleFunc("/api/series", func(w http.ResponseWriter, r *http.Request) {
		from, to := rangeParams(r)
		rows := readRange(from, to)
		step, _ := strconv.Atoi(r.URL.Query().Get("step"))
		if step <= 0 {
			span := int64(0)
			if len(rows) > 1 {
				span = rows[len(rows)-1].t - rows[0].t
			}
			step = autoStep(span, 2500)
		}
		writeJSON(w, bucketize(rows, step))
	})
	mux.HandleFunc("/api/bms", func(w http.ResponseWriter, r *http.Request) {
		from, to := rangeParams(r)
		rows := readRangeP("bms", from, to)
		step, _ := strconv.Atoi(r.URL.Query().Get("step"))
		if step <= 0 {
			span := int64(0)
			if len(rows) > 1 {
				span = rows[len(rows)-1].t - rows[0].t
			}
			step = autoStep(span, 2500)
		}
		writeJSON(w, bucketizeCols(rows, step, bmsColumns))
	})
	mux.HandleFunc("/api/summary", func(w http.ResponseWriter, r *http.Request) {
		from, to := rangeParams(r)
		var out []daySum
		for _, d := range days() {
			if d >= from && d <= to {
				s := summarize(d)
				bmsSummary(&s)
				out = append(out, s)
			}
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		from, to := rangeParams(r)
		writeJSON(w, readEvents(from, to))
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		defer st.mu.Unlock()
		f := func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Format("15:04:05")
		}
		writeJSON(w, map[string]any{"running": st.running, "last": f(st.last), "last_ok": f(st.lastOK),
			"msg": st.msg, "sync": !*noSync, "data": *dataDir})
	})
	mux.HandleFunc("/api/sync", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST", http.StatusMethodNotAllowed)
			return
		}
		go syncOnce()
		writeJSON(w, map[string]bool{"started": true})
	})

	registerSettingsAPI(mux)
	registerExportAPI(mux)

	addr := fmt.Sprintf("127.0.0.1:%d", *port) // local only
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v (уже запущен?)", addr, err)
	}
	url := "http://" + addr + "/"
	log.Printf("open %s", url)
	if !*noBrowse {
		openBrowser(url)
	}
	log.Fatal(http.Serve(ln, guard(mux)))
}
