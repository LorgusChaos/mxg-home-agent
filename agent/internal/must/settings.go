package must

import (
	"fmt"
	"math"
)

// Setting describes one writable inverter setting. This list is the agent's whitelist: nothing else is ever written.
// Register map from github.com/mukaschultze/ha-must-inverter (PV1800). Its ranges contradict values found on this
// inverter (e.g. battery low 17.0 V vs 20–24), so limits here are our own, safe for a 24 V (8S) LiFePO4 pack:
// voltages 20.0–29.2 V (≤3.65 V/cell). Calibration (20009–20016), "clear accumulated data" and grid-export enables
// are deliberately not included.
type Setting struct {
	Reg     uint16   `json:"reg"`
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Group   string   `json:"group"`
	Kind    string   `json:"kind"` // number | select | switch | bit
	Coeff   float64  `json:"coeff,omitempty"`
	Unit    string   `json:"unit,omitempty"`
	Min     float64  `json:"min,omitempty"`
	Max     float64  `json:"max,omitempty"`
	Step    float64  `json:"step,omitempty"`
	Options []string `json:"options,omitempty"` // select: register value = index; "" = not offered
	Bit     int      `json:"bit,omitempty"`     // bit: bit number in Reg (20142)
	Flip    bool     `json:"flip,omitempty"`    // bit: shown value = !bit
	Note    string   `json:"note,omitempty"`
}

const SystemSettingReg = 20142

// Settings is the whitelist, in display order.
var Settings = []Setting{
	// Режимы
	{Reg: 20109, Key: "EnergyUseMode", Name: "Приоритет источника для нагрузки", Group: "Режимы", Kind: "select",
		Options: []string{"", "SBU (панели → батарея → сеть)", "SUB (панели → сеть → батарея)", "UTI (сеть в приоритете)", "SOL (панели в приоритете)"}},
	{Reg: 20143, Key: "ChargerSourcePriority", Name: "Источник заряда батареи", Group: "Режимы", Kind: "select",
		Options: []string{"Сначала панели", "", "Панели и сеть", "Только панели"}},
	{Reg: 20112, Key: "SolarUseAim", Name: "Куда в первую очередь энергия панелей", Group: "Режимы", Kind: "select",
		Options: []string{"LBU: нагрузка → батарея → сеть", "BLU: батарея → нагрузка → сеть"}},
	{Reg: 20111, Key: "GridProtectStandard", Name: "Диапазон входного напряжения сети", Group: "Режимы", Kind: "select",
		Options: []string{"VDE4105", "UPS (узкий)", "APL (широкий)", "GEN (генератор)"}},
	{Reg: 20144, Key: "SolarPowerBalance", Name: "Баланс мощности панелей", Group: "Режимы", Kind: "switch"},
	{Reg: 20104, Key: "InverterSearchModeEnable", Name: "Режим поиска нагрузки (энергосбережение)", Group: "Режимы", Kind: "switch"},
	{Reg: 20101, Key: "InverterOffgridWorkEnable", Name: "Работа инвертора от батареи (off-grid)", Group: "Режимы", Kind: "switch",
		Note: "выключение оставит дом без питания при отключении сети"},

	// Заряд
	{Reg: 10101, Key: "ChargerWorkEnable", Name: "Солнечный контроллер включён", Group: "Заряд", Kind: "switch"},
	{Reg: 10108, Key: "MaxChargerCurrent", Name: "Макс. ток заряда от панелей", Group: "Заряд", Kind: "number", Coeff: 0.1, Unit: "А", Min: 1, Max: 60, Step: 1},
	{Reg: 20125, Key: "GridMaxChargerCurrentSet", Name: "Макс. ток заряда от сети", Group: "Заряд", Kind: "number", Coeff: 0.1, Unit: "А", Min: 1, Max: 60, Step: 1},
	{Reg: 20132, Key: "MaxCombineChargerCurrent", Name: "Макс. суммарный ток заряда", Group: "Заряд", Kind: "number", Coeff: 0.1, Unit: "А", Min: 1, Max: 80, Step: 1},

	// Батарея: пороги переключения
	{Reg: 20118, Key: "BatteryStopDischargingVoltage", Name: "Переход на сеть при напряжении ниже", Group: "Батарея: переключение", Kind: "number", Coeff: 0.1, Unit: "В", Min: 21, Max: 27, Step: 0.1},
	{Reg: 20119, Key: "BatteryStopChargingVoltage", Name: "Возврат на батарею при напряжении выше", Group: "Батарея: переключение", Kind: "number", Coeff: 0.1, Unit: "В", Min: 22, Max: 29, Step: 0.1},
	{Reg: 20127, Key: "InverterBatteryLowVoltage", Name: "Отключение инвертора (низкое напряжение)", Group: "Батарея: переключение", Kind: "number", Coeff: 0.1, Unit: "В", Min: 20, Max: 25, Step: 0.1},

	// Батарея: параметры заряда
	{Reg: 10110, Key: "BatteryType", Name: "Тип батареи", Group: "Батарея: параметры заряда", Kind: "select",
		Options: []string{"", "Пользовательский", "Литиевая", "Свинцово-кислотная", "AGM", "Гелевая", "Залитая"},
		Note: "смена типа может перезаписать напряжения заряда"},
	{Reg: 10104, Key: "AbsorptionVoltage", Name: "Напряжение заряда (bulk/absorption)", Group: "Батарея: параметры заряда", Kind: "number", Coeff: 0.1, Unit: "В", Min: 25, Max: 29.2, Step: 0.1},
	{Reg: 10103, Key: "FloatVoltage", Name: "Напряжение поддержания (float)", Group: "Батарея: параметры заряда", Kind: "number", Coeff: 0.1, Unit: "В", Min: 25, Max: 29.2, Step: 0.1},
	{Reg: 10105, Key: "BatteryLowVoltage", Name: "Низкое напряжение батареи (контроллер)", Group: "Батарея: параметры заряда", Kind: "number", Coeff: 0.1, Unit: "В", Min: 17, Max: 25, Step: 0.1},
	{Reg: 10111, Key: "BatteryAh", Name: "Ёмкость батареи", Group: "Батарея: параметры заряда", Kind: "number", Coeff: 1, Unit: "А·ч", Min: 1, Max: 900, Step: 1},
	{Reg: 10118, Key: "BatteryEqualizationEnable", Name: "Выравнивающий заряд (для LiFePO4 обычно выкл.)", Group: "Батарея: параметры заряда", Kind: "switch"},
	{Reg: 10119, Key: "BatteryEqualizationVoltage", Name: "Напряжение выравнивания", Group: "Батарея: параметры заряда", Kind: "number", Coeff: 0.1, Unit: "В", Min: 25, Max: 29.2, Step: 0.1},
	{Reg: 10121, Key: "BatteryEqualizationTime", Name: "Длительность выравнивания", Group: "Батарея: параметры заряда", Kind: "number", Coeff: 1, Unit: "мин", Min: 5, Max: 900, Step: 5},
	{Reg: 10122, Key: "BatteryEqualizationTimeout", Name: "Таймаут выравнивания", Group: "Батарея: параметры заряда", Kind: "number", Coeff: 1, Unit: "мин", Min: 5, Max: 900, Step: 5},
	{Reg: 10123, Key: "BatteryEqualizationInterval", Name: "Интервал выравнивания", Group: "Батарея: параметры заряда", Kind: "number", Coeff: 1, Unit: "дн", Min: 0, Max: 90, Step: 1},

	// Выход
	{Reg: 20102, Key: "InverterOutputVoltageSet", Name: "Выходное напряжение", Group: "Выход", Kind: "number", Coeff: 0.1, Unit: "В", Min: 220, Max: 240, Step: 1},
	{Reg: 20103, Key: "InverterOutputFrequencySet", Name: "Выходная частота", Group: "Выход", Kind: "number",
		Note: "50 или 60 Гц", Coeff: 0.01, Unit: "Гц", Min: 50, Max: 60, Step: 10},

	// Система (биты регистра 20142)
	{Reg: SystemSettingReg, Key: "OverLoadRestart", Name: "Автоперезапуск после перегрузки", Group: "Система", Kind: "bit", Bit: 0, Flip: true},
	{Reg: SystemSettingReg, Key: "OverTempRestart", Name: "Автоперезапуск после перегрева", Group: "Система", Kind: "bit", Bit: 1, Flip: true},
	{Reg: SystemSettingReg, Key: "OverLoadBypass", Name: "Переход на сеть при перегрузке", Group: "Система", Kind: "bit", Bit: 2, Flip: true},
	{Reg: SystemSettingReg, Key: "AutoTurnPage", Name: "Автолистание экрана", Group: "Система", Kind: "bit", Bit: 3, Flip: true},
	{Reg: SystemSettingReg, Key: "GridBuzz", Name: "Звук при пропадании сети", Group: "Система", Kind: "bit", Bit: 4},
	{Reg: SystemSettingReg, Key: "Buzz", Name: "Звуковой сигнал", Group: "Система", Kind: "bit", Bit: 5, Flip: true},
	{Reg: SystemSettingReg, Key: "LcdLight", Name: "Подсветка экрана всегда", Group: "Система", Kind: "bit", Bit: 6},
	{Reg: SystemSettingReg, Key: "RecordFault", Name: "Запись кодов неисправностей", Group: "Система", Kind: "bit", Bit: 7, Flip: true},
}

// SettingRanges lists the register blocks that hold all settings (read in one go).
var SettingRanges = [][2]uint16{{10101, 24}, {20101, 44}}

// FindSetting returns the setting by key.
func FindSetting(key string) (Setting, bool) {
	for _, s := range Settings {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}

// Display converts a raw register value to the shown value (number: scaled; switch/select: as is; bit: 0/1).
func (s Setting) Display(raw uint16) float64 {
	switch s.Kind {
	case "number":
		c := s.Coeff
		if c == 0 {
			c = 1
		}
		return math.Round(float64(raw)*c*100) / 100
	case "bit":
		v := (raw >> s.Bit) & 1
		if s.Flip {
			v ^= 1
		}
		return float64(v)
	default:
		return float64(raw)
	}
}

// Encode validates a shown value and returns the raw register value to write. For bits, cur is the current
// register value (read-modify-write).
func (s Setting) Encode(v float64, cur uint16) (uint16, error) {
	switch s.Kind {
	case "number":
		if v < s.Min-1e-9 || v > s.Max+1e-9 {
			return 0, fmt.Errorf("%s: %.2f вне диапазона %.2f…%.2f %s", s.Name, v, s.Min, s.Max, s.Unit)
		}
		if s.Key == "InverterOutputFrequencySet" && v != 50 && v != 60 {
			return 0, fmt.Errorf("частота может быть только 50 или 60 Гц")
		}
		c := s.Coeff
		if c == 0 {
			c = 1
		}
		return uint16(math.Round(v / c)), nil
	case "switch":
		if v != 0 && v != 1 {
			return 0, fmt.Errorf("%s: только 0 или 1", s.Name)
		}
		return uint16(v), nil
	case "select":
		i := int(v)
		if float64(i) != v || i < 0 || i >= len(s.Options) || s.Options[i] == "" {
			return 0, fmt.Errorf("%s: недопустимый вариант %v", s.Name, v)
		}
		return uint16(i), nil
	case "bit":
		if v != 0 && v != 1 {
			return 0, fmt.Errorf("%s: только 0 или 1", s.Name)
		}
		b := uint16(v)
		if s.Flip {
			b ^= 1
		}
		if b == 1 {
			return cur | 1<<s.Bit, nil
		}
		return cur &^ (1 << s.Bit), nil
	}
	return 0, fmt.Errorf("неизвестный тип %s", s.Kind)
}

// Format renders a shown value for messages.
func (s Setting) Format(v float64) string {
	switch s.Kind {
	case "select":
		i := int(v)
		if i >= 0 && i < len(s.Options) && s.Options[i] != "" {
			return s.Options[i]
		}
		return fmt.Sprintf("код %d", i)
	case "switch", "bit":
		if v != 0 {
			return "вкл"
		}
		return "выкл"
	default:
		if s.Coeff != 0 && s.Coeff < 1 {
			return fmt.Sprintf("%.1f %s", v, s.Unit)
		}
		return fmt.Sprintf("%.0f %s", v, s.Unit)
	}
}
