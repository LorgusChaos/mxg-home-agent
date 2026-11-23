package rules

import (
	"errors"
	"strings"
	"testing"
	"time"

	"mxg/internal/must"
)

func snap(t time.Time, gridV, battV float64) must.Snapshot {
	return must.Snapshot{Time: t, GridV: gridV, BattV: battV, LoadW: 300}
}

func TestGridDebounceAndDuration(t *testing.T) {
	e := New(Config{})
	t0 := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	var all []string
	step := func(i int, g float64) {
		all = append(all, Texts(e.OnReading(snap(t0.Add(time.Duration(i)*10*time.Second), g, 26.2)))...)
	}
	for i := 0; i < 5; i++ {
		step(i, 230) // grid known as present, no message
	}
	step(5, 0) // single glitch: ignored
	step(6, 230)
	if len(all) != 0 {
		t.Fatalf("unexpected: %v", all)
	}
	for i := 7; i < 10; i++ {
		step(i, 0)
	}
	if len(all) != 1 || !strings.Contains(all[0], "пропал") {
		t.Fatalf("want outage message, got %v", all)
	}
	for i := 10; i < 370; i++ { // ~1 h
		step(i, 0)
	}
	for i := 370; i < 373; i++ {
		step(i, 230)
	}
	last := all[len(all)-1]
	if !strings.Contains(last, "вернулся") || !strings.Contains(last, "1 ч") {
		t.Fatalf("want restore with ~1h, got %q", last)
	}
}

func TestBatteryBands(t *testing.T) {
	e := New(Config{SmoothN: 1})
	t0 := time.Now()
	var msgs []string
	for i, v := range []float64{26.4, 26.0, 25.9, 25.8, 25.7, 26.0, 26.2} { // per cell 3.30 .. 3.21 .. back up
		msgs = append(msgs, Texts(e.OnReading(snap(t0.Add(time.Duration(i)*time.Second), 230, v)))...)
	}
	down, up := 0, 0
	for _, m := range msgs {
		if strings.Contains(m, "ниже") {
			down++
		}
		if strings.Contains(m, "заряжена") {
			up++
		}
	}
	if down < 2 || up < 1 {
		t.Fatalf("down=%d up=%d msgs=%v", down, up, msgs)
	}
}

func TestCommAlarm(t *testing.T) {
	e := New(Config{})
	t0 := time.Now()
	e.OnReading(snap(t0, 230, 26.4))
	if m := e.OnError(t0.Add(time.Minute), errors.New("x")); m != nil {
		t.Fatalf("too early: %v", m)
	}
	if m := e.OnError(t0.Add(3*time.Minute), errors.New("x")); len(m) != 1 || m[0].Kind != "comm_lost" {
		t.Fatalf("want alarm, got %v", m)
	}
	if m := e.OnError(t0.Add(4*time.Minute), errors.New("x")); m != nil {
		t.Fatalf("repeat alarm: %v", m)
	}
	if m := Texts(e.OnReading(snap(t0.Add(5*time.Minute), 230, 26.4))); len(m) == 0 || !strings.Contains(m[0], "восстановлена") {
		t.Fatalf("want restore, got %v", m)
	}
}

func TestFaults(t *testing.T) {
	e := New(Config{})
	t0 := time.Now()
	read := func(i int, errInv, errChg uint32) []Event {
		s := snap(t0.Add(time.Duration(i)*10*time.Second), 230, 26.4)
		s.ErrInv, s.ErrChg = errInv, errChg
		return e.OnReading(s)
	}
	var evs []Event
	evs = append(evs, read(0, 0, 0)...)
	evs = append(evs, read(1, 1<<6, 0)...) // single glitch
	evs = append(evs, read(2, 0, 0)...)
	evs = append(evs, read(3, 0, 0)...)
	for _, ev := range evs {
		if strings.HasPrefix(ev.Kind, "fault") {
			t.Fatalf("glitch produced %v", ev)
		}
	}
	evs = append(read(4, 1<<6, 1<<5), read(5, 1<<6, 1<<5)...) // overload + night "PV too low"
	var notified, quiet int
	for _, ev := range evs {
		if ev.Kind == "fault_set" && ev.Notify && strings.Contains(ev.Text, "перегрузка") {
			notified++
		}
		if ev.Kind == "info_set" && !ev.Notify && strings.Contains(ev.Text, "панелей") {
			quiet++
		}
	}
	if notified != 1 || quiet != 1 {
		t.Fatalf("notified=%d quiet=%d evs=%v", notified, quiet, evs)
	}
	evs = append(read(6, 0, 1<<5), read(7, 0, 1<<5)...)
	if len(evs) != 1 || evs[0].Kind != "fault_clear" || !evs[0].Notify {
		t.Fatalf("want one clear, got %v", evs)
	}
}
