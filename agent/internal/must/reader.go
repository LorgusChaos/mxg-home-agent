//go:build linux

package must

import (
	"fmt"
	"time"

	"mxg/internal/ch340"
	"mxg/internal/proto"
	"mxg/internal/usbfs"
)

// Reader keeps the CH340 port open and reopens it after errors.
type Reader struct {
	port       *ch340.Port
	controller string // USB controller of the CH340 (remembered for power-cycling)
}

func (r *Reader) ensure() error {
	if r.port != nil {
		return nil
	}
	p, err := ch340.Open(Baud)
	if err != nil {
		return err
	}
	r.port = p
	if c, err := usbfs.ControllerOf(p.SysPath()); err == nil {
		r.controller = c
	}
	return nil
}

// ResetUSB power-cycles the port the CH340 hangs on (after mains returns the chip can stop answering even
// descriptor reads). Uses the remembered controller, or looks the device up again.
func (r *Reader) ResetUSB() (string, error) {
	r.Close()
	ctrl := r.controller
	if ctrl == "" {
		sys, err := usbfs.Find(ch340.VID, ch340.PID)
		if err != nil {
			return "", fmt.Errorf("controller unknown and device not found: %w", err)
		}
		if ctrl, err = usbfs.ControllerOf(sys); err != nil {
			return "", err
		}
	}
	if err := usbfs.PowerCycle(ctrl, 3*time.Second); err != nil {
		return ctrl, err
	}
	time.Sleep(3 * time.Second) // re-enumeration
	return ctrl, nil
}

// Close closes the port.
func (r *Reader) Close() {
	if r.port != nil {
		r.port.Close()
		r.port = nil
	}
}

func (r *Reader) readRegs(start, count uint16) ([]uint16, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		r.port.Drain()
		if err := r.port.Write(proto.ModbusRead(Slave, 3, start, count)); err != nil {
			return nil, err // USB-level error: caller reopens
		}
		resp, err := r.port.ReadUntil(1500*time.Millisecond, 200*time.Millisecond, func(b []byte) bool {
			l := proto.ModbusResponseLen(b)
			return l > 0 && len(b) >= l
		})
		if err != nil {
			return nil, err
		}
		regs, exc, err := proto.ModbusParse(resp)
		switch {
		case err != nil:
			lastErr = fmt.Errorf("reg %d: %w (%d bytes)", start, err, len(resp))
		case exc != 0:
			return nil, fmt.Errorf("reg %d: modbus exception %d", start, exc)
		case len(regs) != int(count):
			lastErr = fmt.Errorf("reg %d: got %d regs, want %d", start, len(regs), count)
		default:
			return regs, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, lastErr
}

// ReadBlock reads count holding registers starting at start.
func (r *Reader) ReadBlock(start, count uint16) ([]uint16, error) {
	if err := r.ensure(); err != nil {
		return nil, fmt.Errorf("open CH340: %w", err)
	}
	regs, err := r.readRegs(start, count)
	if err != nil {
		r.Close()
	}
	return regs, err
}

// WriteReg writes one register (function 6) and reads it back. Returns the value read back.
func (r *Reader) WriteReg(reg, value uint16) (uint16, error) {
	if err := r.ensure(); err != nil {
		return 0, fmt.Errorf("open CH340: %w", err)
	}
	req := proto.ModbusWrite(Slave, reg, value)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		r.port.Drain()
		if err := r.port.Write(req); err != nil {
			r.Close()
			return 0, err
		}
		resp, err := r.port.ReadUntil(2*time.Second, 200*time.Millisecond, func(b []byte) bool {
			l := proto.ModbusWriteResponseLen(b)
			return l > 0 && len(b) >= l
		})
		if err != nil {
			r.Close()
			return 0, err
		}
		exc, err := proto.ModbusWriteCheck(req, resp)
		if err == nil && exc != 0 {
			return 0, fmt.Errorf("инвертор отклонил запись (modbus exception %d)", exc)
		}
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = fmt.Errorf("write %d: %w", reg, err)
		time.Sleep(300 * time.Millisecond)
	}
	if lastErr != nil {
		return 0, lastErr
	}
	// the inverter applies a setting with a delay (seen: >0.3 s for 10103): poll until it shows up, up to ~6 s
	var last uint16
	var readErr error
	for i := 0; i < 12; i++ {
		time.Sleep(500 * time.Millisecond)
		back, err := r.readRegs(reg, 1)
		if err != nil {
			readErr = err
			continue
		}
		last, readErr = back[0], nil
		if last == value {
			return last, nil
		}
	}
	if readErr != nil {
		return 0, fmt.Errorf("записано, но не удалось прочитать обратно: %w", readErr)
	}
	return last, nil
}

// Read takes one snapshot.
func (r *Reader) Read() (Snapshot, error) {
	if err := r.ensure(); err != nil {
		return Snapshot{}, fmt.Errorf("open CH340: %w", err)
	}
	inv, err := r.readRegs(InvStart, InvCount)
	if err != nil {
		r.Close()
		return Snapshot{}, err
	}
	chg, err := r.readRegs(ChgStart, ChgCount)
	if err != nil {
		r.Close()
		return Snapshot{}, err
	}
	return Decode(time.Now(), inv, chg), nil
}
