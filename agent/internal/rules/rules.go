// Package rules turns a stream of inverter readings into owner notifications (events only, no periodic spam).
package rules

import (
	"fmt"
	"math"
	"time"

	"mxg/internal/must"
	"mxg/internal/soc"
)

// Event is something worth recording; Notify says whether the owner gets a message.
type Event struct {
	Time   time.Time
	Kind   string // grid_lost, grid_back, batt_down, batt_up, comm_lost, comm_back, fault_set, fault_clear, warn_set, warn_clear
	Text   string
	Notify bool
}

// Texts returns the texts of events to notify.
func Texts(evs []Event) []string {
	var out []string
	for _, e := range evs {
		if e.Notify {
			out = append(out, e.Text)
		}
	}
	return out
}

// maskTrack debounces a fault/warning bit mask: a value counts once seen twice in a row.
type maskTrack struct {
	prev, active uint32
	seen         bool
}

func (m *maskTrack) update(raw uint32) (set, cleared uint32) {
	if !m.seen {
		m.prev, m.seen = raw, true
		return 0, 0
	}
	if raw != m.prev {
		m.prev = raw
		return 0, 0
	}
	set, cleared = raw&^m.active, m.active&^raw
	m.active = raw
	return set, cleared
}

// Config tunes the rules.
type Config struct {
	Cells        int           // LiFePO4 cells in series (8 for 24 V)
	GridDebounce int           // equal readings in a row needed to accept a grid change
	CommTimeout  time.Duration // no successful read this long -> alarm
	SmoothN      int           // readings in the voltage median window
}

// Engine holds rule state. Not safe for concurrent use.
type Engine struct {
	cfg Config

	gridKnown   bool
	grid        bool
	gridPending bool
	gridCount   int
	gridSince   time.Time

	volts []float64
	band  int // announced 10%-band (-1 = not yet)

	lastOK     time.Time
	commAlarm  bool
	commStart  time.Time
	started    bool
	lastSOC    float64
	lastSample must.Snapshot

	errInv, warnInv, errChg, warnChg maskTrack

	// BMS (JK) state
	bmsSOC      float64
	bmsTime     time.Time
	bmsLost     bool
	bmsSeen     bool
	bmsErr      maskTrack
	bmsChg      int // -1 unknown, 0 off, 1 on (debounced through two equal readings)
	bmsDis      int
	bmsChgPrev  int
	bmsDisPrev  int
	deltaAlarm  bool
	lowCellWarn bool

	// BMS current transitions: hints for when mains came back / went away while the inverter link was down
	bmsCur          float64
	bmsCurKnown     bool
	bmsDischarging  bool
	dischargeStop   time.Time // discharge (< -2 A) ended
	dischargeStart  time.Time // discharge began
	gridBlind       bool      // a grid change may have happened while we had no inverter data
	gridBlindFrom   time.Time
}

// BMSFresh reports whether BMS data is recent enough to trust (60 s).
func (e *Engine) BMSFresh(now time.Time) bool {
	return !e.bmsTime.IsZero() && now.Sub(e.bmsTime) < time.Minute
}

// ActiveFaults returns the currently active (debounced) masks: inverter errors, inverter warnings,
// charger errors, charger warnings.
func (e *Engine) ActiveFaults() (uint32, uint32, uint32, uint32) {
	return e.errInv.active, e.warnInv.active, e.errChg.active, e.warnChg.active
}

func (e *Engine) faultEvents(t time.Time, s must.Snapshot) []Event {
	var out []Event
	type src struct {
		tr    *maskTrack
		raw   uint32
		table []string
		what  string
		err   bool
		quiet uint32
	}
	for _, x := range []src{
		{&e.errInv, s.ErrInv, must.InverterErrors, "инвертора", true, 0},
		{&e.warnInv, s.WarnInv, must.InverterWarnings, "инвертора", false, 0},
		{&e.errChg, s.ErrChg, must.ChargerErrors, "солнечного контроллера", true, must.QuietChargerErrors},
		{&e.warnChg, s.WarnChg, must.ChargerWarnings, "солнечного контроллера", false, 0},
	} {
		set, cleared := x.tr.update(x.raw)
		for i := 0; i < 32; i++ {
			bit := uint32(1) << i
			if set&bit == 0 && cleared&bit == 0 {
				continue
			}
			text := must.Describe(bit, x.table)
			notify := x.quiet&bit == 0
			switch {
			case !notify && set&bit != 0: // normal condition (e.g. no PV at night): log only, no alarm icon
				out = append(out, Event{t, "info_set", fmt.Sprintf("ℹ️ %s (контроллер, обычное состояние)", text), false})
			case !notify:
				out = append(out, Event{t, "info_clear", fmt.Sprintf("ℹ️ Снято: %s (контроллер)", text), false})
			case set&bit != 0 && x.err:
				out = append(out, Event{t, "fault_set", fmt.Sprintf("❗ Ошибка %s: %s", x.what, text), notify})
			case set&bit != 0:
				out = append(out, Event{t, "warn_set", fmt.Sprintf("⚠️ Предупреждение %s: %s", x.what, text), notify})
			case x.err:
				out = append(out, Event{t, "fault_clear", fmt.Sprintf("✅ Ошибка %s снята: %s", x.what, text), notify})
			default:
				out = append(out, Event{t, "warn_clear", fmt.Sprintf("ℹ️ Предупреждение %s снято: %s", x.what, text), notify})
			}
		}
	}
	return out
}

// New creates an engine.
func New(cfg Config) *Engine {
	if cfg.Cells == 0 {
		cfg.Cells = 8
	}
	if cfg.GridDebounce == 0 {
		cfg.GridDebounce = 3
	}
	if cfg.CommTimeout == 0 {
		cfg.CommTimeout = 2 * time.Minute
	}
	if cfg.SmoothN == 0 {
		cfg.SmoothN = 6
	}
	return &Engine{cfg: cfg, band: -1, bmsChg: -1, bmsDis: -1, bmsChgPrev: -1, bmsDisPrev: -1}
}

// BMSReading is the part of the JK BMS data the rules use.
type BMSReading struct {
	Time                   time.Time
	SOC                    int
	CellMin, CellMax       float64
	CellMinIdx, CellMaxIdx int
	CellDelta              float64
	Errors                 uint32
	ChargeOn, DischargeOn  bool
	Current                float64 // A, + charge
}

// OnBMS processes fresh BMS data. quiet: error bits that are only logged.
func (e *Engine) OnBMS(b BMSReading, errTexts func(uint32) string, quiet uint32) []Event {
	var out []Event
	t := b.Time
	if e.bmsLost {
		out = append(out, Event{t, "bms_back", "✅ Связь с BMS восстановлена", true})
		e.bmsLost = false
	}
	e.bmsSeen = true
	e.bmsSOC, e.bmsTime = float64(b.SOC), t
	// discharging state with hysteresis (< -3 A on, > -1.5 A off); real data steps -10.2, -1.08, -0.72, 0, +38
	if !e.bmsCurKnown {
		e.bmsDischarging = b.Current < -3
	} else if !e.bmsDischarging && b.Current < -3 {
		e.bmsDischarging, e.dischargeStart = true, t
	} else if e.bmsDischarging && b.Current > -1.5 {
		e.bmsDischarging, e.dischargeStop = false, t
	}
	e.bmsCur, e.bmsCurKnown = b.Current, true

	set, cleared := e.bmsErr.update(b.Errors)
	for i := 0; i < 32; i++ {
		bit := uint32(1) << i
		if set&bit == 0 && cleared&bit == 0 {
			continue
		}
		text := errTexts(bit)
		switch {
		case quiet&bit != 0 && set&bit != 0:
			out = append(out, Event{t, "info_set", "ℹ️ BMS: " + text, false})
		case quiet&bit != 0:
			out = append(out, Event{t, "info_clear", "ℹ️ BMS снято: " + text, false})
		case set&bit != 0:
			out = append(out, Event{t, "bms_fault", "❗ BMS: " + text, true})
		default:
			out = append(out, Event{t, "bms_fault_clear", "✅ BMS снято: " + text, true})
		}
	}

	onoff := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	sw := func(cur *int, prev *int, v int, what string) {
		if v != *prev { // needs two equal readings in a row
			*prev = v
			return
		}
		if *cur == -1 {
			*cur = v
			if v == 0 {
				out = append(out, Event{t, "bms_switch", "❗ BMS: " + what + " выключен", true})
			}
			return
		}
		if v != *cur {
			*cur = v
			if v == 0 {
				out = append(out, Event{t, "bms_switch", "❗ BMS: " + what + " выключен", true})
			} else {
				out = append(out, Event{t, "bms_switch", "✅ BMS: " + what + " включён", true})
			}
		}
	}
	sw(&e.bmsChg, &e.bmsChgPrev, onoff(b.ChargeOn), "ключ заряда")
	sw(&e.bmsDis, &e.bmsDisPrev, onoff(b.DischargeOn), "ключ разряда (батарея отключена от нагрузки)")

	if !e.deltaAlarm && b.CellDelta >= 0.1 {
		e.deltaAlarm = true
		out = append(out, Event{t, "bms_delta", fmt.Sprintf("⚠️ Разбаланс ячеек %.3f В (ячейка %d: %.3f, ячейка %d: %.3f)",
			b.CellDelta, b.CellMinIdx, b.CellMin, b.CellMaxIdx, b.CellMax), true})
	} else if e.deltaAlarm && b.CellDelta < 0.07 {
		e.deltaAlarm = false
		out = append(out, Event{t, "bms_delta", fmt.Sprintf("✅ Разбаланс ячеек в норме: %.3f В", b.CellDelta), true})
	}
	if !e.lowCellWarn && b.CellMin > 0 && b.CellMin < 2.9 {
		e.lowCellWarn = true
		out = append(out, Event{t, "bms_lowcell", fmt.Sprintf("⚠️ Низкое напряжение ячейки %d: %.3f В", b.CellMinIdx, b.CellMin), true})
	} else if e.lowCellWarn && b.CellMin >= 3.05 {
		e.lowCellWarn = false
	}
	return out
}

// OnBMSMissing is called when no fresh BMS data is available.
func (e *Engine) OnBMSMissing(now time.Time) []Event {
	if !e.bmsSeen || e.bmsLost || e.bmsTime.IsZero() || now.Sub(e.bmsTime) < 5*time.Minute {
		return nil
	}
	e.bmsLost = true
	return []Event{{now, "bms_lost", "⚠️ Нет данных от BMS больше 5 мин (батарея оценивается по напряжению)", true}}
}

// SOC returns the BMS state of charge when fresh, else the smoothed voltage estimate (percent), -1 if unknown.
func (e *Engine) SOC() float64 {
	if e.BMSFresh(time.Now()) {
		return e.bmsSOC
	}
	if len(e.volts) == 0 {
		return -1
	}
	return e.lastSOC
}

// VoltSOC returns only the voltage-based estimate (kept in data-*.csv as soc_est).
func (e *Engine) VoltSOC() float64 {
	if len(e.volts) == 0 {
		return -1
	}
	return e.lastSOC
}

func (e *Engine) socAt(now time.Time) (float64, string) {
	if e.BMSFresh(now) {
		return e.bmsSOC, "BMS"
	}
	return e.lastSOC, "≈ по напряжению"
}

// GridState returns (known, present, since).
func (e *Engine) GridState() (bool, bool, time.Time) { return e.gridKnown, e.grid, e.gridSince }

func dur(d time.Duration) string {
	d = d.Round(time.Minute)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%d ч %02d мин", h, m)
	}
	return fmt.Sprintf("%d мин", m)
}

func (e *Engine) battLine(s must.Snapshot) string {
	p, src := e.socAt(s.Time)
	return fmt.Sprintf("Батарея %.0f%% (%s), %.1f В, нагрузка %d Вт", p, src, s.BattV, s.LoadW)
}

// OnReading processes a successful reading and returns events.
func (e *Engine) OnReading(s must.Snapshot) []Event {
	var out []Event
	now := s.Time
	e.lastSample = s
	ev := func(kind, text string) { out = append(out, Event{now, kind, text, true}) }

	if e.commAlarm {
		ev("comm_back", fmt.Sprintf("✅ Связь с инвертором восстановлена (не было %s)", dur(now.Sub(e.commStart))))
		e.commAlarm = false
	}
	prevOK := e.lastOK
	e.lastOK = now
	blind := !prevOK.IsZero() && now.Sub(prevOK) > time.Minute // the inverter link was down until now

	// battery estimate
	e.volts = append(e.volts, s.BattV)
	if len(e.volts) > e.cfg.SmoothN {
		e.volts = e.volts[len(e.volts)-e.cfg.SmoothN:]
	}
	e.lastSOC = soc.FromVoltage(soc.Median(e.volts), e.cfg.Cells)

	// grid with debounce
	present := s.GridPresent()
	switch {
	case e.gridKnown && present == e.grid:
		e.gridBlind = false // state unchanged (also across a gap): nothing missed
	case blind:
		e.gridBlind, e.gridBlindFrom = true, prevOK // a change may have happened during this gap
	}
	if !e.gridKnown {
		if e.gridCount == 0 || present != e.gridPending {
			e.gridPending, e.gridCount = present, 1
		} else {
			e.gridCount++
		}
		if e.gridCount >= e.cfg.GridDebounce {
			e.gridKnown, e.grid, e.gridSince = true, present, now
		}
	} else if present != e.grid {
		if present != e.gridPending || e.gridCount == 0 {
			e.gridPending, e.gridCount = present, 1
		} else {
			e.gridCount++
		}
		if e.gridCount >= e.cfg.GridDebounce {
			at, note := now, ""
			if e.gridBlind {
				// we did not see the moment: take it from the BMS current (discharge stopped / started)
				hint := e.dischargeStart
				if present {
					hint = e.dischargeStop
				}
				if hint.After(e.gridSince) && hint.Before(now) && !hint.Before(e.gridBlindFrom) {
					at, note = hint, " (время по BMS: связи с инвертором не было)"
				} else {
					note = fmt.Sprintf(" (замечено после перерыва связи с инвертором с %s, точное время неизвестно)",
						e.gridBlindFrom.Format("15:04"))
				}
			}
			was := at.Sub(e.gridSince)
			e.grid, e.gridSince, e.gridCount, e.gridBlind = present, at, 0, false
			if present {
				ev("grid_back", fmt.Sprintf("💡 Свет вернулся в %s%s. Не было %s.\n%s",
					at.Format("15:04"), note, dur(was), e.battLine(s)))
			} else {
				ev("grid_lost", fmt.Sprintf("⚡ Свет пропал в %s%s.\n%s",
					at.Format("15:04"), note, e.battLine(s)))
			}
		}
	} else {
		e.gridCount = 0
	}

	// battery 10%-bands with 5% hysteresis on the way up; BMS SOC when fresh, else the voltage estimate after a
	// full smoothing window
	if bmsOK := e.BMSFresh(now); bmsOK || len(e.volts) >= e.cfg.SmoothN {
		p, src := e.socAt(now)
		if e.band < 0 {
			e.band = int(math.Floor(p / 10))
		} else if p < float64(e.band*10) {
			e.band = int(math.Floor(p / 10))
			lvl := (e.band + 1) * 10
			icon := "🔋"
			if lvl <= 20 {
				icon = "⚠️"
			}
			ev("batt_down", fmt.Sprintf("%s Батарея ниже %d%%: %.0f%% (%s), %.1f В, нагрузка %d Вт",
				icon, lvl, p, src, s.BattV, s.LoadW))
		} else if p >= float64((e.band+1)*10+5) {
			e.band = int(math.Floor((p - 5) / 10))
			ev("batt_up", fmt.Sprintf("🔋 Батарея заряжена до %d%%: %.0f%% (%s), %.1f В", e.band*10, p, src, s.BattV))
		}
	}

	out = append(out, e.faultEvents(now, s)...)

	if !e.started && e.gridKnown {
		e.started = true
	}
	return out
}

// OnError processes a failed reading.
func (e *Engine) OnError(now time.Time, err error) []Event {
	if e.lastOK.IsZero() {
		e.lastOK = now // count the timeout from agent start
	}
	if !e.commAlarm && now.Sub(e.lastOK) >= e.cfg.CommTimeout {
		e.commAlarm = true
		e.commStart = e.lastOK
		return []Event{{now, "comm_lost", fmt.Sprintf("❗ Нет связи с инвертором больше %s: %v", dur(e.cfg.CommTimeout), err), true}}
	}
	return nil
}
