//go:build linux

// Package ch340 is a userspace driver for WCH CH340/CH341 USB-serial chips over usbfs.
// The init sequence and baud divisor follow Linux drivers/usb/serial/ch341.c.
package ch340

import (
	"errors"
	"fmt"
	"time"

	"mxg/internal/usbfs"
)

const (
	VID = 0x1a86
	PID = 0x7523

	reqReadVersion = 0x5F
	reqWriteReg    = 0x9A
	reqSerialInit  = 0xA1
	reqModemCtrl   = 0xA4

	regPrescaler = 0x12
	regDivisor   = 0x13
	regLCR       = 0x18
	regLCR2      = 0x25

	lcrEnableRX = 0x80
	lcrEnableTX = 0x40
	lcrCS8      = 0x03

	bitDTR = 1 << 5
	bitRTS = 1 << 6

	ctrlTO = 1000 // ms
)

// Port is an open CH340 serial port.
type Port struct {
	dev     *usbfs.Device
	epIn    uint8
	epOut   uint8
	Version uint16
	Baud    int
}

func (p *Port) out(req uint8, value, index uint16) error {
	_, err := p.dev.Control(0x40, req, value, index, nil, ctrlTO)
	if err != nil {
		return fmt.Errorf("ctrl out %02x %04x %04x: %w", req, value, index, err)
	}
	return nil
}

func (p *Port) in(req uint8, value, index uint16, n int) ([]byte, error) {
	b := make([]byte, n)
	got, err := p.dev.Control(0xC0, req, value, index, b, ctrlTO)
	if err != nil {
		return nil, fmt.Errorf("ctrl in %02x: %w", req, err)
	}
	return b[:got], nil
}

// Open finds the CH340, claims it and configures baud, 8N1, DTR+RTS on.
func Open(baud int) (*Port, error) {
	sys, err := usbfs.Find(VID, PID)
	if err != nil {
		return nil, err
	}
	dev, err := usbfs.Open(sys)
	if err != nil {
		return nil, err
	}
	p := &Port{dev: dev, epIn: 0x82, epOut: 0x02}
	for a, t := range dev.Endpoints() {
		if t == "Bulk" {
			if a&0x80 != 0 {
				p.epIn = a
			} else {
				p.epOut = a
			}
		}
	}
	if err := dev.Claim(0); err != nil {
		dev.Close()
		return nil, fmt.Errorf("claim: %w", err)
	}
	v, err := p.in(reqReadVersion, 0, 0, 2)
	if err != nil {
		dev.Close()
		return nil, err
	}
	if len(v) >= 1 {
		p.Version = uint16(v[0])
	}
	if err := p.out(reqSerialInit, 0, 0); err != nil {
		dev.Close()
		return nil, err
	}
	if err := p.SetBaud(baud); err != nil {
		dev.Close()
		return nil, err
	}
	if err := p.out(reqModemCtrl, ^uint16(bitDTR|bitRTS), 0); err != nil {
		dev.Close()
		return nil, err
	}
	return p, nil
}

// SetBaud sets the baud rate with 8N1 framing.
func (p *Port) SetBaud(baud int) error {
	val, err := divisor(baud)
	if err != nil {
		return err
	}
	if p.Version > 0x27 {
		val |= 1 << 7 // do not buffer until a full 32-byte packet
	}
	if err := p.out(reqWriteReg, regDivisor<<8|regPrescaler, val); err != nil {
		return err
	}
	if p.Version >= 0x30 { // older chips do not support writing LCR (always 8N1)
		if err := p.out(reqWriteReg, regLCR2<<8|regLCR, lcrEnableRX|lcrEnableTX|lcrCS8); err != nil {
			return err
		}
	}
	p.Baud = baud
	return nil
}

// Write sends bytes.
func (p *Port) Write(b []byte) error {
	for len(b) > 0 {
		n, err := p.dev.Bulk(p.epOut, b, 2000)
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

// ReadChunk reads what arrives within timeout (may return 0 bytes and nil error on timeout).
func (p *Port) ReadChunk(timeout time.Duration) ([]byte, error) {
	b := make([]byte, 256)
	n, err := p.dev.Bulk(p.epIn, b, uint32(timeout/time.Millisecond))
	if errors.Is(err, usbfs.ErrTimeout) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return b[:n], nil
}

// ReadUntil collects bytes until stop returns true, or until total timeout / idle gap expires.
func (p *Port) ReadUntil(total, idle time.Duration, stop func([]byte) bool) ([]byte, error) {
	var buf []byte
	deadline := time.Now().Add(total)
	for time.Now().Before(deadline) {
		wait := idle
		if len(buf) == 0 {
			wait = time.Until(deadline)
		}
		if wait < 10*time.Millisecond {
			wait = 10 * time.Millisecond
		}
		chunk, err := p.ReadChunk(wait)
		if err != nil {
			return buf, err
		}
		if len(chunk) == 0 {
			if len(buf) > 0 {
				return buf, nil // idle gap after data
			}
			continue
		}
		buf = append(buf, chunk...)
		if stop != nil && stop(buf) {
			return buf, nil
		}
	}
	return buf, nil
}

// Drain discards pending input.
func (p *Port) Drain() {
	for i := 0; i < 20; i++ {
		c, err := p.ReadChunk(50 * time.Millisecond)
		if err != nil || len(c) == 0 {
			return
		}
	}
}

// Close releases the device.
func (p *Port) Close() error { return p.dev.Close() }

// SysPath is the sysfs path of the USB device.
func (p *Port) SysPath() string { return p.dev.SysPath }
