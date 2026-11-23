// Package store writes readings to daily CSV files: data-YYYY-MM-DD.csv (every poll, main fields) and
// raw-YYYY-MM-DD.csv (raw registers, sparse) for later checks of the register map.
package store

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"mxg/internal/jkbms"
	"mxg/internal/must"
)

// Header of data-*.csv. Changing it renames an existing day file with another header to data-DAY.vN.csv.
const header = "time,work_state,grid_v,out_v,out_hz,batt_v,soc_est,load_w,load_pct,inv_w,grid_w,pv_v,pv_w,chg_a,bus_v," +
	"temp_a,temp_b,temp_chg,temp_dc,batt_w,batt_a,err_inv,warn_inv,err_chg,warn_chg," +
	"e_load,e_buy,e_pv,e_discharge,e_grid_charge,e_charge"

const eventsHeader = "time,kind,notified,text"

// Store appends rows to per-day files in Dir.
type Store struct {
	Dir string
}

// firstLine reads the header of an existing file.
func firstLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	if sc.Scan() {
		return sc.Text()
	}
	return ""
}

func (s *Store) appendLine(prefix string, t time.Time, hdr, line string) error {
	name := filepath.Join(s.Dir, prefix+"-"+t.Format("2006-01-02")+".csv")
	if hdr != "" {
		if old := firstLine(name); old != "" && old != hdr {
			for v := 1; ; v++ {
				alt := strings.TrimSuffix(name, ".csv") + ".v" + strconv.Itoa(v) + ".csv"
				if _, err := os.Stat(alt); os.IsNotExist(err) {
					os.Rename(name, alt)
					break
				}
			}
		}
	}
	_, statErr := os.Stat(name)
	f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if os.IsNotExist(statErr) && hdr != "" {
		if _, err := f.WriteString(hdr + "\n"); err != nil {
			return err
		}
	}
	_, err = f.WriteString(line + "\n")
	return err
}

// Write appends the main row.
func (s *Store) Write(r must.Snapshot, socEst float64) error {
	e := r.Energy
	line := fmt.Sprintf("%s,%d,%.1f,%.1f,%.2f,%.1f,%.0f,%d,%d,%d,%d,%.1f,%d,%.1f,%.1f,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,"+
		"%.1f,%.1f,%.1f,%.1f,%.1f,%.1f",
		r.Time.Format("2006-01-02 15:04:05"), r.WorkState, r.GridV, r.OutV, r.OutHz, r.BattV, socEst,
		r.LoadW, r.LoadPct, r.InvW, r.GridW, r.PvV, r.PvW, r.ChgA, r.BusV, r.TempA, r.TempB, r.ChgTemp,
		r.TempDC, r.BattW, r.BattA, r.ErrInv, r.WarnInv, r.ErrChg, r.WarnChg,
		e.Load, e.Buy, e.PV, e.Discharger, e.GridCharger, e.Charger)
	return s.appendLine("data", r.Time, header, line)
}

const bmsHeader = "time,soc,voltage,current,power,remaining_ah,capacity_ah,cell_min,cell_max,cell_delta,cell_avg," +
	"cell_min_idx,cell_max_idx,temp_mos,temp1,temp2,balancing,balance_a,errors,charge_on,discharge_on,cycles,soh," +
	"c1,c2,c3,c4,c5,c6,c7,c8,c9,c10,c11,c12,c13,c14,c15,c16"

// WriteBMS appends a JK BMS reading to bms-DAY.csv.
func (s *Store) WriteBMS(t time.Time, c jkbms.CellInfo) error {
	b01 := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s,%d,%.3f,%.3f,%.1f,%.3f,%.3f,%.3f,%.3f,%.3f,%.3f,%d,%d,%.1f,%.1f,%.1f,%d,%.3f,%d,%d,%d,%d,%d",
		t.Format("2006-01-02 15:04:05"), c.SOC, c.Voltage, c.Current, c.Power, c.RemainingAh, c.CapacityAh,
		c.CellMin, c.CellMax, c.CellDelta, c.CellAvg, c.CellMinIdx, c.CellMaxIdx, c.TempMOS, c.Temp1, c.Temp2,
		c.Balancing, c.BalanceA, c.Errors, b01(c.ChargeOn), b01(c.DischargeOn), c.Cycles, c.SOH)
	for i := 0; i < 16; i++ {
		b.WriteByte(',')
		if i < len(c.Cells) {
			fmt.Fprintf(&b, "%.3f", c.Cells[i])
		}
	}
	return s.appendLine("bms", t, bmsHeader, b.String())
}

// WriteEvent appends to events.csv (one file, the agent's own event log).
func (s *Store) WriteEvent(t time.Time, kind string, notified bool, text string) error {
	name := filepath.Join(s.Dir, "events.csv")
	_, statErr := os.Stat(name)
	f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if os.IsNotExist(statErr) {
		f.WriteString(eventsHeader + "\n")
	}
	clean := strings.NewReplacer("\n", " | ", "\"", "'").Replace(text)
	_, err = fmt.Fprintf(f, "%s,%s,%t,\"%s\"\n", t.Format("2006-01-02 15:04:05"), kind, notified, clean)
	return err
}

// WriteRaw appends raw registers: time, then 25201.., then 15201..
func (s *Store) WriteRaw(r must.Snapshot) error {
	var b strings.Builder
	b.WriteString(r.Time.Format("2006-01-02 15:04:05"))
	for _, v := range r.Inv {
		b.WriteByte(',')
		b.WriteString(strconv.Itoa(int(v)))
	}
	for _, v := range r.Chg {
		b.WriteByte(',')
		b.WriteString(strconv.Itoa(int(v)))
	}
	var hdr strings.Builder
	hdr.WriteString("time")
	for i := range r.Inv {
		fmt.Fprintf(&hdr, ",r%d", must.InvStart+i)
	}
	for i := range r.Chg {
		fmt.Fprintf(&hdr, ",r%d", must.ChgStart+i)
	}
	return s.appendLine("raw", r.Time, hdr.String(), b.String())
}

// Export concatenates the main CSV of the last `days` days (today included) into one file and returns its path.
func (s *Store) Export(days int, now time.Time) (string, int, error) {
	// only current-format day files (data-DAY.vN.csv have an older header)
	files, _ := filepath.Glob(filepath.Join(s.Dir, "data-[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9].csv"))
	sort.Strings(files)
	from := now.AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	out := filepath.Join(s.Dir, fmt.Sprintf("export-%s-%dd.csv", now.Format("2006-01-02"), days))
	o, err := os.Create(out)
	if err != nil {
		return "", 0, err
	}
	defer o.Close()
	w := bufio.NewWriter(o)
	w.WriteString(header + "\n")
	rows := 0
	for _, f := range files {
		day := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "data-"), ".csv")
		if day < from {
			continue
		}
		in, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(in)
		first := true
		for sc.Scan() {
			if first {
				first = false
				continue // header
			}
			w.WriteString(sc.Text() + "\n")
			rows++
		}
		in.Close()
	}
	return out, rows, w.Flush()
}
