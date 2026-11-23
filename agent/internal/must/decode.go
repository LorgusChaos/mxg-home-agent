// Package must reads and decodes the Must PV1800-series inverter (PV18-3224 VPM II) over Modbus RTU.
// Register map: docs/must_modbus.md. Fields marked "?" there are tentative; raw registers are kept for later checks.
package must

import (
	"math"
	"time"
)

const (
	Slave    = 4
	Baud     = 19200
	InvStart = 25201
	InvCount = 80
	ChgStart = 15201
	ChgCount = 21
)

// Energy holds accumulated counters, kWh (register pairs hi*1000 + lo*0.1).
type Energy struct {
	Charger, Discharger, Buy, Sell, Load, SelfUse, PvSell, GridCharger float64 // 25245..25260
	PV                                                                   float64 // 15217..15218
}

// Snapshot is one decoded reading.
type Snapshot struct {
	Time      time.Time
	WorkState int     // 25201
	RatedVA   int     // 25203
	BattV     float64 // 25205
	OutV      float64 // 25206
	GridV     float64 // 25207
	BusV      float64 // 25208
	InvA      float64 // 25210
	GridA     float64 // 25211 ?
	LoadA     float64 // 25212 ?
	InvW      int     // 25213 ?
	GridW     int     // 25214 ?
	LoadW     int     // 25215 ?
	LoadPct   int     // 25216
	OutHz     float64 // 25225
	GridHz    float64 // 25226
	TempA     int     // 25233 AC radiator
	TempB     int     // 25234 transformer
	TempDC    int     // 25235 DC radiator
	BattW     int     // 25273 battery power (sign: to be checked against the display)
	BattA     int     // 25274 battery current, A
	ErrInv    uint32  // 25261 | 25262<<16
	WarnInv   uint32  // 25265
	ErrChg    uint32  // 15213
	WarnChg   uint32  // 15214
	Energy    Energy

	ChgState  int     // 15201 ?
	MPPTState int     // 15202 ?
	ChgPhase  int     // 15203 ?
	PvV       float64 // 15205
	ChgBattV  float64 // 15206
	ChgA      float64 // 15207 ?
	PvW       int     // 15208 ?
	ChgTemp   int     // 15209

	Inv []uint16 // raw 25201..25280
	Chg []uint16 // raw 15201..15220
}

var workStates = map[int]string{
	0: "включение", 1: "самотест", 2: "от батареи", 3: "параллельно с сетью",
	4: "от сети (байпас)", 5: "остановлен", 6: "заряд от сети",
}

// WorkStateName returns a human name of the work state.
func WorkStateName(s int) string {
	if n, ok := workStates[s]; ok {
		return n
	}
	return "неизвестно"
}

func at(r []uint16, i int) uint16 {
	if i < len(r) {
		return r[i]
	}
	return 0
}

// Decode builds a Snapshot from the two raw register blocks.
func Decode(t time.Time, inv, chg []uint16) Snapshot {
	s16 := func(r []uint16, i int) int { return int(int16(at(r, i))) }
	d10 := func(r []uint16, i int) float64 { return float64(at(r, i)) / 10 }
	kwh := func(r []uint16, i int) float64 {
		return math.Round((float64(at(r, i))*1000+float64(at(r, i+1))*0.1)*10) / 10
	}
	return Snapshot{
		TempDC:  s16(inv, 34),
		BattW:   s16(inv, 72),
		BattA:   s16(inv, 73),
		ErrInv:  uint32(at(inv, 60)) | uint32(at(inv, 61))<<16,
		WarnInv: uint32(at(inv, 64)),
		ErrChg:  uint32(at(chg, 12)),
		WarnChg: uint32(at(chg, 13)),
		Energy: Energy{
			Charger: kwh(inv, 44), Discharger: kwh(inv, 46), Buy: kwh(inv, 48), Sell: kwh(inv, 50),
			Load: kwh(inv, 52), SelfUse: kwh(inv, 54), PvSell: kwh(inv, 56), GridCharger: kwh(inv, 58),
			PV: kwh(chg, 16),
		},
		Time:      t,
		WorkState: int(at(inv, 0)),
		RatedVA:   int(at(inv, 2)),
		BattV:     d10(inv, 4),
		OutV:      d10(inv, 5),
		GridV:     d10(inv, 6),
		BusV:      d10(inv, 7),
		InvA:      d10(inv, 9),
		GridA:     d10(inv, 10),
		LoadA:     d10(inv, 11),
		InvW:      s16(inv, 12),
		GridW:     s16(inv, 13),
		LoadW:     s16(inv, 14),
		LoadPct:   int(at(inv, 15)),
		OutHz:     float64(at(inv, 24)) / 100,
		GridHz:    float64(at(inv, 25)) / 100,
		TempA:     s16(inv, 32),
		TempB:     s16(inv, 33),
		ChgState:  int(at(chg, 0)),
		MPPTState: int(at(chg, 1)),
		ChgPhase:  int(at(chg, 2)),
		PvV:       d10(chg, 4),
		ChgBattV:  d10(chg, 5),
		ChgA:      d10(chg, 6),
		PvW:       s16(chg, 7),
		ChgTemp:   s16(chg, 8),
		Inv:       inv,
		Chg:       chg,
	}
}

// GridPresent reports whether mains voltage is present.
func (s Snapshot) GridPresent() bool { return s.GridV >= 150 }
