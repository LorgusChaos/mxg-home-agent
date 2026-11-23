package ch340

import "testing"

// Values from the old fixed table in Linux ch341.c.
func TestDivisor(t *testing.T) {
	for baud, want := range map[int]uint16{2400: 0xd901, 4800: 0x6402, 9600: 0xb202, 19200: 0xd902, 38400: 0x6403} {
		got, err := divisor(baud)
		if err != nil || got != want {
			t.Errorf("divisor(%d) = %#04x, %v; want %#04x", baud, got, err, want)
		}
	}
}
