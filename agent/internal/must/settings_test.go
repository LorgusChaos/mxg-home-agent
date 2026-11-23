package must

import "testing"

func TestSettingsEncodeDisplay(t *testing.T) {
	get := func(k string) Setting {
		s, ok := FindSetting(k)
		if !ok {
			t.Fatalf("no setting %s", k)
		}
		return s
	}
	// values read from a real PV18-3224 VPM II
	if v := get("FloatVoltage").Display(273); v != 27.3 {
		t.Fatalf("float %v", v)
	}
	if r, err := get("FloatVoltage").Encode(27.4, 0); err != nil || r != 274 {
		t.Fatalf("encode float %d %v", r, err)
	}
	if _, err := get("FloatVoltage").Encode(30, 0); err == nil {
		t.Fatal("30 V must be refused")
	}
	if _, err := get("EnergyUseMode").Encode(0, 0); err == nil {
		t.Fatal("empty option must be refused")
	}
	if r, _ := get("EnergyUseMode").Encode(1, 0); r != 1 {
		t.Fatal("SBU = 1")
	}
	// SystemSetting 20142 = 48 (bits 4,5); Buzz has flip: bit5=1 -> shown off
	if v := get("Buzz").Display(48); v != 0 {
		t.Fatalf("buzz shown %v", v)
	}
	if r, _ := get("Buzz").Encode(1, 48); r != 16 { // turn buzzer on = clear bit 5
		t.Fatalf("buzz on -> %d", r)
	}
	if r, _ := get("LcdLight").Encode(1, 48); r != 48|64 {
		t.Fatalf("lcd on -> %d", r)
	}
	if _, err := get("InverterOutputFrequencySet").Encode(55, 0); err == nil {
		t.Fatal("55 Hz must be refused")
	}
	if r, _ := get("InverterOutputFrequencySet").Encode(50, 0); r != 5000 {
		t.Fatalf("50 Hz -> %d", r)
	}
	// every whitelisted register must lie inside SettingRanges
	for _, s := range Settings {
		in := false
		for _, rg := range SettingRanges {
			if s.Reg >= rg[0] && s.Reg < rg[0]+rg[1] {
				in = true
			}
		}
		if !in {
			t.Errorf("%s (%d) outside SettingRanges", s.Key, s.Reg)
		}
	}
}
