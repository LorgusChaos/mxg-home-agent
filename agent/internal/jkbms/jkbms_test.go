package jkbms

import (
	"os"
	"testing"
)

func load(t *testing.T, name string) []byte {
	s, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseHex(string(s))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return b
}

// Real frames captured from JK-B2A8S20P (hw V19H, sw V19.27) (device serial masked).
func TestRealFrames(t *testing.T) {
	c, err := DecodeCellInfo(load(t, "frame_02.hex"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("cell info: %+v", c)
	if len(c.Cells) != 8 {
		t.Fatalf("want 8 cells, got %d", len(c.Cells))
	}
	if c.CellMin < 3.0 || c.CellMax > 3.65 || c.CellDelta > 0.1 {
		t.Fatalf("cells implausible: %+v", c.Cells)
	}
	if c.Voltage < 24 || c.Voltage > 29.2 {
		t.Fatalf("voltage %v", c.Voltage)
	}
	if c.SOC < 0 || c.SOC > 100 || c.SOH < 0 || c.SOH > 100 {
		t.Fatalf("soc %d soh %d", c.SOC, c.SOH)
	}
	d, err := DecodeDeviceInfo(load(t, "frame_03.hex"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("device: %+v", d)
	// serial and name bytes were replaced with a placeholder in testdata (checksum recomputed)
	if d.Serial != "TESTSERIAL00001" || d.Model != "JK-B2A8S20P" || d.HW != "19H" {
		t.Fatalf("serial %q", d.Serial)
	}
	s, err := DecodeSettings(load(t, "frame_01.hex"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("settings: %+v", s)
	if s.CellCount != 8 {
		t.Fatalf("cell count %d", s.CellCount)
	}
}
