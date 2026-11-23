package must

import (
	"strings"
	"testing"
	"time"
)

// Real blocks read from a PV18-3224 VPM II: 25201.. and 15201..
var inv = []uint16{2, 230, 3200, 1, 267, 2299, 0, 4100, 0, 19, 0, 15, 298, 0, 241, 12, 382, 0, 303, 0, 238, 0, 258, 0, 5000,
	0, 0, 0, 0, 0, 0, 0, 42, 50, 0, 0, 1, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 3177, 1, 9902, 0, 0, 1, 6176, 0, 3215, 0, 0, 0, 6064,
	0, 0, 0, 0, 0, 0, 0, 0, 65535, 65535, 10101, 22534, 355, 10, 24, 65530, 3200, 10414, 166, 65427}
var chg = []uint16{1, 0, 0, 0, 0, 262, 0, 0, 25, 0, 0, 0, 32, 0, 24, 600, 0, 0, 0, 0, 0}

func TestDecodeReal(t *testing.T) {
	s := Decode(time.Now(), inv, chg)
	if s.BattV != 26.7 || s.OutV != 229.9 || s.LoadW != 241 || s.OutHz != 50 {
		t.Fatalf("basic fields: %+v", s)
	}
	if s.BattW != 355 || s.BattA != 10 {
		t.Fatalf("battery: %d W %d A", s.BattW, s.BattA)
	}
	if s.Energy.Buy != 1990.2 || s.Energy.Load != 1617.6 || s.Energy.Discharger != 317.7 {
		t.Fatalf("energy: %+v", s.Energy)
	}
	if s.ErrInv != 0 || s.WarnInv != 0 || s.ErrChg != 32 {
		t.Fatalf("faults: %d %d %d", s.ErrInv, s.WarnInv, s.ErrChg)
	}
}

func TestDescribe(t *testing.T) {
	if Describe(0, InverterErrors) != "нет" {
		t.Fatal("zero")
	}
	d := Describe(1<<3|1<<16, InverterErrors)
	if !strings.Contains(d, "слишком низкое") || !strings.Contains(d, "перегрев радиатора") {
		t.Fatalf("got %q", d)
	}
	if !strings.Contains(Describe(1<<31, InverterErrors), "бит 31") {
		t.Fatal("unknown bit")
	}
}
