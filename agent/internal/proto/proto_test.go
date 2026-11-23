package proto

import (
	"bytes"
	"testing"
)

func TestVoltronicFrame(t *testing.T) {
	// widely published Voltronic frames
	for cmd, want := range map[string][]byte{
		"QPIGS": {'Q', 'P', 'I', 'G', 'S', 0xb7, 0xa9, '\r'},
		"QMOD":  {'Q', 'M', 'O', 'D', 0x49, 0xc1, '\r'},
		"QPI":   {'Q', 'P', 'I', 0xbe, 0xac, '\r'},
	} {
		if got := VoltronicFrame(cmd); !bytes.Equal(got, want) {
			t.Errorf("%s: got % x want % x", cmd, got, want)
		}
	}
}

func TestVoltronicParseRoundTrip(t *testing.T) {
	resp := VoltronicFrame("(B")
	p, ok, err := VoltronicParse(resp)
	if err != nil || !ok || p != "B" {
		t.Fatalf("got %q %v %v", p, ok, err)
	}
}

func TestModbus(t *testing.T) {
	want := []byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x0a, 0xc5, 0xcd}
	if got := ModbusRead(1, 3, 0, 10); !bytes.Equal(got, want) {
		t.Errorf("got % x want % x", got, want)
	}
	// response: slave 1, fn 3, 2 bytes, value 0x1234 + CRC
	r := []byte{0x01, 0x03, 0x02, 0x12, 0x34}
	c := CRC16Modbus(r)
	r = append(r, byte(c), byte(c>>8))
	regs, exc, err := ModbusParse(r)
	if err != nil || exc != 0 || len(regs) != 1 || regs[0] != 0x1234 {
		t.Fatalf("parse: %v %d %v", regs, exc, err)
	}
}
