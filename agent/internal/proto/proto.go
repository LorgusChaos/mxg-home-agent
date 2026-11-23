// Package proto builds and checks frames of the two protocols inverters of this class use over serial:
// Voltronic/Axpert ASCII commands (QPIGS, QMOD, ...) and Modbus RTU.
package proto

import (
	"encoding/binary"
	"fmt"
)

// CRC16XModem is CRC-16/XMODEM (poly 0x1021, init 0), used by Voltronic.
func CRC16XModem(b []byte) uint16 {
	var crc uint16
	for _, c := range b {
		crc ^= uint16(c) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// voltronic CRC bytes must not collide with '(' CR LF: such bytes are incremented.
func voltronicCRCBytes(b []byte) [2]byte {
	c := CRC16XModem(b)
	out := [2]byte{byte(c >> 8), byte(c)}
	for i, v := range out {
		if v == 0x28 || v == 0x0d || v == 0x0a {
			out[i] = v + 1
		}
	}
	return out
}

// VoltronicFrame returns cmd + CRC + CR.
func VoltronicFrame(cmd string) []byte {
	b := []byte(cmd)
	c := voltronicCRCBytes(b)
	return append(append(b, c[0], c[1]), '\r')
}

// VoltronicParse checks a response "(....CRC\r" and returns the payload without '(' and CRC.
func VoltronicParse(resp []byte) (payload string, crcOK bool, err error) {
	if len(resp) < 4 || resp[len(resp)-1] != '\r' {
		return "", false, fmt.Errorf("no CR terminator")
	}
	body := resp[:len(resp)-3]
	got := [2]byte{resp[len(resp)-3], resp[len(resp)-2]}
	crcOK = got == voltronicCRCBytes(body)
	if len(body) > 0 && body[0] == '(' {
		body = body[1:]
	}
	return string(body), crcOK, nil
}

// CRC16Modbus is CRC-16/MODBUS (reflected poly 0xA001, init 0xFFFF), sent low byte first.
func CRC16Modbus(b []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, c := range b {
		crc ^= uint16(c)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// ModbusRead builds a read request (fn 3 holding or 4 input registers).
func ModbusRead(slave, fn uint8, reg, count uint16) []byte {
	b := []byte{slave, fn, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(b[2:], reg)
	binary.BigEndian.PutUint16(b[4:], count)
	c := CRC16Modbus(b)
	return append(b, byte(c), byte(c>>8))
}

// ModbusWrite builds a write-single-register request (function 6). The reply echoes the request.
func ModbusWrite(slave uint8, reg, value uint16) []byte {
	b := []byte{slave, 6, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(b[2:], reg)
	binary.BigEndian.PutUint16(b[4:], value)
	c := CRC16Modbus(b)
	return append(b, byte(c), byte(c>>8))
}

// ModbusWriteResponseLen is 8 for a normal echo, 5 for an exception.
func ModbusWriteResponseLen(b []byte) int {
	if len(b) < 2 {
		return 0
	}
	if b[1]&0x80 != 0 {
		return 5
	}
	return 8
}

// ModbusWriteCheck validates the reply to ModbusWrite(req).
func ModbusWriteCheck(req, resp []byte) (exception uint8, err error) {
	if len(resp) >= 5 && resp[1]&0x80 != 0 {
		c := CRC16Modbus(resp[:3])
		if resp[3] != byte(c) || resp[4] != byte(c>>8) {
			return 0, fmt.Errorf("bad CRC")
		}
		return resp[2], nil
	}
	if len(resp) < 8 {
		return 0, fmt.Errorf("short reply (%d bytes)", len(resp))
	}
	for i := 0; i < 8; i++ {
		if resp[i] != req[i] {
			return 0, fmt.Errorf("reply does not echo the request")
		}
	}
	return 0, nil
}

// ModbusResponseLen returns the full expected length once the header is known (0 if not yet known).
func ModbusResponseLen(b []byte) int {
	if len(b) < 3 {
		return 0
	}
	if b[1]&0x80 != 0 {
		return 5 // exception: slave fn code crcLo crcHi
	}
	return 3 + int(b[2]) + 2
}

// ModbusParse checks CRC and returns register values (or exception code).
func ModbusParse(b []byte) (regs []uint16, exception uint8, err error) {
	n := ModbusResponseLen(b)
	if n == 0 || len(b) < n {
		return nil, 0, fmt.Errorf("short frame (%d bytes)", len(b))
	}
	b = b[:n]
	c := CRC16Modbus(b[:n-2])
	if b[n-2] != byte(c) || b[n-1] != byte(c>>8) {
		return nil, 0, fmt.Errorf("bad CRC")
	}
	if b[1]&0x80 != 0 {
		return nil, b[2], nil
	}
	for i := 3; i+1 < n-2; i += 2 {
		regs = append(regs, binary.BigEndian.Uint16(b[i:]))
	}
	return regs, 0, nil
}
