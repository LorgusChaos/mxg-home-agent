package rules

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Real case: overnight outage; mains returned at 17:56:59 and the CH340 hung the same second; the inverter link
// came back at 18:54. The return time must come from the BMS current, not 18:54.
func TestGridReturnDuringInverterGap(t *testing.T) {
	e := New(Config{})
	loc := time.FixedZone("EET", 2*3600)
	t0 := time.Date(2026, 11, 22, 22, 0, 0, 0, loc)
	at := func(h, m, s int) time.Time { return time.Date(2026, 11, 23, h, m, s, 0, loc) }
	// grid known absent since 22:34 (day before)
	for i := 0; i < 4; i++ {
		e.OnReading(snap(t0.Add(time.Duration(i)*10*time.Second), 0, 26.4))
	}
	e.gridSince = time.Date(2026, 11, 22, 22, 34, 0, 0, loc)
	last := at(17, 56, 49)
	e.OnReading(snap(last, 0, 26.4))
	bms(t0, 54, 0.003, true, true, 0)
	// BMS keeps reporting: discharging, then charging from 17:56:59
	e.OnBMS(BMSReading{Time: at(17, 56, 49), SOC: 54, Current: -10.2, ChargeOn: true, DischargeOn: true}, txt, 0)
	e.OnBMS(BMSReading{Time: at(17, 56, 59), SOC: 54, Current: -1.0, ChargeOn: true, DischargeOn: true}, txt, 0)
	e.OnBMS(BMSReading{Time: at(17, 58, 0), SOC: 54, Current: 38.6, ChargeOn: true, DischargeOn: true}, txt, 0)
	e.OnError(at(18, 0, 0), errors.New("usb"))
	var msgs []string
	for i := 0; i < 3; i++ {
		ts := at(18, 54, 19).Add(time.Duration(i) * 10 * time.Second)
		e.OnBMS(BMSReading{Time: ts, SOC: 64, Current: 36, ChargeOn: true, DischargeOn: true}, txt, 0)
		msgs = append(msgs, Texts(e.OnReading(snap(ts, 168, 27.3)))...)
	}
	got := strings.Join(msgs, "|")
	if !strings.Contains(got, "Свет вернулся в 17:56") || !strings.Contains(got, "по BMS") || !strings.Contains(got, "19 ч 23 мин") {
		t.Fatalf("got %q", got)
	}
}
