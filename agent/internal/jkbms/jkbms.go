// Package jkbms decodes JK BMS BLE frames (protocol JK02_32S, hardware 11+/19.x; this unit: JK-B2A8S20P hw V19H).
// Offsets per github.com/syssi/esphome-jk-bms components/jk_bms_ble/jk_bms_ble.cpp (decode_jk02_cell_info_ with
// offset 16, fields after 112 shifted by +32). Frames: 300 bytes, 55 AA EB 90, type at [4], byte 299 = sum mod 256.
package jkbms

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
)

const FrameLen = 300

// CellInfo is a decoded 0x02 frame.
type CellInfo struct {
	Cells       []float64 `json:"cells"`       // V, only enabled cells
	Resistances []float64 `json:"resistances"` // Ω
	CellMin     float64   `json:"cell_min"`
	CellMax     float64   `json:"cell_max"`
	CellMinIdx  int       `json:"cell_min_idx"` // 1-based
	CellMaxIdx  int       `json:"cell_max_idx"`
	CellDelta   float64   `json:"cell_delta"`
	CellAvg     float64   `json:"cell_avg"`
	Voltage     float64   `json:"voltage"` // V
	Current     float64   `json:"current"` // A, + charge / − discharge
	Power       float64   `json:"power"`   // W, + charge
	TempMOS     float64   `json:"temp_mos"`
	Temp1       float64   `json:"temp1"`
	Temp2       float64   `json:"temp2"`
	Errors      uint32    `json:"errors"`
	BalanceA    float64   `json:"balance_a"`
	Balancing   int       `json:"balancing"` // 0 off, 1 charging, 2 discharging
	SOC         int       `json:"soc"`       // %
	RemainingAh float64   `json:"remaining_ah"`
	CapacityAh  float64   `json:"capacity_ah"`
	Cycles      uint32    `json:"cycles"`
	CycleAh     float64   `json:"cycle_ah"` // total charged Ah
	SOH         int       `json:"soh"`
	Uptime      uint32    `json:"uptime_s"`
	ChargeOn    bool      `json:"charge_on"`
	DischargeOn bool      `json:"discharge_on"`
	Heating     bool      `json:"heating"`
}

// DeviceInfo is a decoded 0x03 frame.
type DeviceInfo struct {
	Model, HW, SW, Name, Serial, MfgDate string
	Uptime, PowerOns                      uint32
}

// Settings is part of a decoded 0x01 frame.
type Settings struct {
	CellUVP, CellUVPRecovery, CellOVP, CellOVPRecovery float64 // V
	BalanceTrigger, SOC100V, SOC0V, PowerOffV          float64 // V
	MaxChargeA, MaxDischargeA, MaxBalanceA             float64
	CellCount                                          int
	ChargeSwitch, DischargeSwitch, BalancerSwitch      bool
	CapacityAh, BalanceStartV                          float64
}

// ErrorTexts: bit -> text (DEFAULT_ERRORS_JK02, translated).
var ErrorTexts = []string{
	"сопротивление проводов", "перегрев MOSFET", "число ячеек не совпадает с настройкой", "",
	"батарея полностью заряжена", "перенапряжение батареи", "перегрузка по току заряда", "КЗ при заряде",
	"перегрев при заряде", "низкая температура при заряде", "ошибка связи с сопроцессором", "низкое напряжение ячейки",
	"низкое напряжение батареи", "перегрузка по току разряда", "КЗ при разряде", "перегрев при разряде",
	"неисправен MOSFET заряда", "неисправен MOSFET разряда", "GPS отключён", "смените пароль",
	"не удалось включить разряд", "перегрев батареи", "неисправен датчик температуры", "неисправен модуль PL",
	"не удалось снять защиту от КЗ", "перегрузка разряда II", "перегрузка разряда III", "низкая температура при разряде",
	"удалённая блокировка GPS", "", "", "",
}

// QuietErrors are informational bits (bit 4 "fully charged"): logged, not alarmed.
const QuietErrors uint32 = 1 << 4

// DescribeErrors lists texts of set bits.
func DescribeErrors(mask uint32) string {
	if mask == 0 {
		return "нет"
	}
	var out []string
	for i := 0; i < 32; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		if i < len(ErrorTexts) && ErrorTexts[i] != "" {
			out = append(out, ErrorTexts[i])
		} else {
			out = append(out, fmt.Sprintf("бит %d", i))
		}
	}
	return strings.Join(out, "; ")
}

// ParseHex turns the bridge's hex text into a checked frame.
func ParseHex(s string) ([]byte, error) {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, err
	}
	return b, Check(b)
}

// Check validates length, start marker and checksum.
func Check(b []byte) error {
	if len(b) < FrameLen {
		return fmt.Errorf("frame too short: %d", len(b))
	}
	if b[0] != 0x55 || b[1] != 0xAA || b[2] != 0xEB || b[3] != 0x90 {
		return fmt.Errorf("bad start marker")
	}
	var sum byte
	for _, c := range b[:FrameLen-1] {
		sum += c
	}
	if sum != b[FrameLen-1] {
		return fmt.Errorf("bad checksum")
	}
	return nil
}

func u16(b []byte, o int) uint16 { return binary.LittleEndian.Uint16(b[o:]) }
func i16(b []byte, o int) int16  { return int16(u16(b, o)) }
func u32(b []byte, o int) uint32 { return binary.LittleEndian.Uint32(b[o:]) }
func i32(b []byte, o int) int32  { return int32(u32(b, o)) }
func r3(x float64) float64       { return math.Round(x*1000) / 1000 }

func cstr(b []byte, o, n int) string {
	s := b[o : o+n]
	if i := strings.IndexByte(string(s), 0); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(string(s))
}

// DecodeCellInfo decodes a 0x02 frame (JK02_32S).
func DecodeCellInfo(b []byte) (CellInfo, error) {
	if err := Check(b); err != nil {
		return CellInfo{}, err
	}
	if b[4] != 0x02 {
		return CellInfo{}, fmt.Errorf("not a cell info frame (type %#x)", b[4])
	}
	var c CellInfo
	enabled := u32(b, 70)
	c.CellMin, c.CellMax = 100, -100
	sum, n := 0.0, 0
	for i := 0; i < 32; i++ {
		v := float64(u16(b, 6+2*i)) / 1000
		if enabled&(1<<i) == 0 && v == 0 {
			continue
		}
		c.Cells = append(c.Cells, v)
		c.Resistances = append(c.Resistances, float64(u16(b, 80+2*i))/1000)
		if v > 0 {
			sum += v
			n++
			if v < c.CellMin {
				c.CellMin, c.CellMinIdx = v, i+1
			}
		}
		if v > c.CellMax {
			c.CellMax, c.CellMaxIdx = v, i+1
		}
	}
	if n > 0 {
		c.CellAvg = r3(sum / float64(n))
		c.CellDelta = r3(c.CellMax - c.CellMin)
	} else {
		c.CellMin, c.CellMax = 0, 0
	}
	c.TempMOS = float64(i16(b, 144)) / 10
	c.Voltage = float64(u32(b, 150)) / 1000
	c.Current = float64(i32(b, 158)) / 1000
	c.Power = math.Round(c.Voltage*c.Current*10) / 10
	c.Temp1 = float64(i16(b, 162)) / 10
	c.Temp2 = float64(i16(b, 164)) / 10
	c.Errors = u32(b, 166)
	c.BalanceA = float64(i16(b, 170)) / 1000
	c.Balancing = int(b[172])
	c.SOC = int(b[173])
	c.RemainingAh = float64(u32(b, 174)) / 1000
	c.CapacityAh = float64(u32(b, 178)) / 1000
	c.Cycles = u32(b, 182)
	c.CycleAh = float64(u32(b, 186)) / 1000
	c.SOH = int(b[190])
	c.Uptime = u32(b, 194)
	c.ChargeOn = b[198] != 0
	c.DischargeOn = b[199] != 0
	c.Heating = b[215] != 0
	return c, nil
}

// DecodeDeviceInfo decodes a 0x03 frame.
func DecodeDeviceInfo(b []byte) (DeviceInfo, error) {
	if err := Check(b); err != nil {
		return DeviceInfo{}, err
	}
	if b[4] != 0x03 {
		return DeviceInfo{}, fmt.Errorf("not a device info frame (type %#x)", b[4])
	}
	d := DeviceInfo{
		Model: cstr(b, 6, 16), HW: cstr(b, 22, 8), SW: cstr(b, 30, 8),
		Uptime: u32(b, 38), PowerOns: u32(b, 42), Name: cstr(b, 46, 16), Serial: cstr(b, 86, 11),
	}
	if b[78] != 0 {
		d.MfgDate = "20" + cstr(b, 78, 6)
	}
	// the serial field holds 11 bytes; this unit's full serial (15 chars) is in the name field
	if strings.HasPrefix(d.Name, d.Serial) && len(d.Name) > len(d.Serial) {
		d.Serial = d.Name
	}
	return d, nil
}

// DecodeSettings decodes the main fields of a 0x01 frame (JK02_32S).
func DecodeSettings(b []byte) (Settings, error) {
	if err := Check(b); err != nil {
		return Settings{}, err
	}
	if b[4] != 0x01 {
		return Settings{}, fmt.Errorf("not a settings frame (type %#x)", b[4])
	}
	mv := func(o int) float64 { return float64(u32(b, o)) / 1000 }
	return Settings{
		CellUVP: mv(10), CellUVPRecovery: mv(14), CellOVP: mv(18), CellOVPRecovery: mv(22),
		BalanceTrigger: mv(26), SOC100V: mv(30), SOC0V: mv(34), PowerOffV: mv(46),
		MaxChargeA: mv(50), MaxDischargeA: mv(62), MaxBalanceA: mv(78),
		CellCount: int(b[114]), ChargeSwitch: b[118] != 0, DischargeSwitch: b[122] != 0, BalancerSwitch: b[126] != 0,
		CapacityAh: mv(130), BalanceStartV: mv(138),
	}, nil
}
