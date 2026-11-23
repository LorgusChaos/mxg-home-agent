package rules

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func bms(t time.Time, soc int, delta float64, chg, dis bool, errs uint32) BMSReading {
	return BMSReading{Time: t, SOC: soc, CellMin: 3.3, CellMax: 3.3 + delta, CellMinIdx: 1, CellMaxIdx: 2,
		CellDelta: delta, Errors: errs, ChargeOn: chg, DischargeOn: dis}
}

func txt(m uint32) string { return fmt.Sprintf("bits %d", m) }

func TestBMSRules(t *testing.T) {
	e := New(Config{})
	t0 := time.Now()
	var evs []Event
	step := func(i int, b BMSReading) {
		b.Time = t0.Add(time.Duration(i) * 10 * time.Second)
		evs = append(evs, e.OnBMS(b, txt, 1<<4)...)
	}
	step(0, bms(t0, 86, 0.003, true, true, 0))
	step(1, bms(t0, 86, 0.003, true, true, 0))
	if len(Texts(evs)) != 0 {
		t.Fatalf("quiet start expected, got %v", evs)
	}
	step(2, bms(t0, 85, 0.003, true, false, 0)) // discharge MOSFET off: needs 2 readings
	if len(Texts(evs)) != 0 {
		t.Fatalf("glitch alarmed: %v", evs)
	}
	step(3, bms(t0, 85, 0.003, true, false, 0))
	got := strings.Join(Texts(evs), "|")
	if !strings.Contains(got, "ключ разряда") {
		t.Fatalf("want discharge switch alarm, got %q", got)
	}
	evs = nil
	step(4, bms(t0, 85, 0.12, true, true, 1<<4))
	step(5, bms(t0, 85, 0.12, true, true, 1<<4))
	got = strings.Join(Texts(evs), "|")
	if !strings.Contains(got, "Разбаланс") || strings.Contains(got, "bits 16") {
		t.Fatalf("want delta alarm and quiet bit 4 not notified, got %q", got)
	}
	// BMS SOC drives SOC() while fresh
	if e.socAtTest(t0.Add(55*time.Second)) != 85 {
		t.Fatal("BMS SOC not used")
	}
	if m := e.OnBMSMissing(t0.Add(10 * time.Minute)); len(m) != 1 {
		t.Fatalf("want bms_lost, got %v", m)
	}
}

func (e *Engine) socAtTest(now time.Time) float64 { p, _ := e.socAt(now); return p }
