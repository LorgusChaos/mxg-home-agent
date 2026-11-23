//go:build linux

// Package usbfs talks to a USB device through Linux usbfs (/dev/bus/usb/BBB/DDD) without a kernel driver.
package usbfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// Layouts match struct usbdevfs_ctrltransfer / usbdevfs_bulktransfer for the build's pointer size.
type ctrlTransfer struct {
	RequestType uint8
	Request     uint8
	Value       uint16
	Index       uint16
	Length      uint16
	Timeout     uint32 // ms
	Data        unsafe.Pointer
}

type bulkTransfer struct {
	Ep      uint32
	Len     uint32
	Timeout uint32 // ms
	Data    unsafe.Pointer
}

func ioc(dir, typ, nr, size uintptr) uintptr { return dir<<30 | size<<16 | typ<<8 | nr }

var (
	ioctlControl   = ioc(3, 'U', 0, unsafe.Sizeof(ctrlTransfer{}))
	ioctlBulk      = ioc(3, 'U', 2, unsafe.Sizeof(bulkTransfer{}))
	ioctlClaim     = ioc(2, 'U', 15, 4)
	ioctlRelease   = ioc(2, 'U', 16, 4)
	ErrTimeout     = errors.New("usb timeout")
	sysfsUSBDevice = "/sys/bus/usb/devices"
)

// Device is an open usbfs device node.
type Device struct {
	f       *os.File
	SysPath string // e.g. /sys/bus/usb/devices/1-1
	claimed []uint32
}

func readHex(path string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(b)), 16, 32)
}

func readDec(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// Find returns the sysfs path of the first device with the given VID:PID.
func Find(vid, pid uint16) (string, error) {
	entries, err := os.ReadDir(sysfsUSBDevice)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		p := filepath.Join(sysfsUSBDevice, e.Name())
		v, err1 := readHex(filepath.Join(p, "idVendor"))
		d, err2 := readHex(filepath.Join(p, "idProduct"))
		if err1 == nil && err2 == nil && uint16(v) == vid && uint16(d) == pid {
			return p, nil
		}
	}
	return "", fmt.Errorf("usb device %04x:%04x not found", vid, pid)
}

// Open opens the usbfs node of the device at sysfs path sys.
func Open(sys string) (*Device, error) {
	bus, err := readDec(filepath.Join(sys, "busnum"))
	if err != nil {
		return nil, err
	}
	dev, err := readDec(filepath.Join(sys, "devnum"))
	if err != nil {
		return nil, err
	}
	node := fmt.Sprintf("/dev/bus/usb/%03d/%03d", bus, dev)
	f, err := os.OpenFile(node, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return &Device{f: f, SysPath: sys}, nil
}

func (d *Device) ioctl(req uintptr, arg unsafe.Pointer) (int, error) {
	r, _, e := syscall.Syscall(syscall.SYS_IOCTL, d.f.Fd(), req, uintptr(arg))
	if e != 0 {
		if e == syscall.ETIMEDOUT {
			return int(r), ErrTimeout
		}
		return int(r), e
	}
	return int(r), nil
}

// Claim claims an interface for this process.
func (d *Device) Claim(iface uint32) error {
	v := iface
	_, err := d.ioctl(ioctlClaim, unsafe.Pointer(&v))
	if err == nil {
		d.claimed = append(d.claimed, iface)
	}
	return err
}

// Control does a control transfer. For IN transfers (requestType&0x80) data is filled; returns bytes transferred.
func (d *Device) Control(requestType, request uint8, value, index uint16, data []byte, timeoutMs uint32) (int, error) {
	buf := make([]byte, len(data)+1) // heap buffer, never empty
	copy(buf, data)
	ct := &ctrlTransfer{RequestType: requestType, Request: request, Value: value, Index: index,
		Length: uint16(len(data)), Timeout: timeoutMs, Data: unsafe.Pointer(&buf[0])}
	n, err := d.ioctl(ioctlControl, unsafe.Pointer(ct))
	runtime.KeepAlive(ct)
	runtime.KeepAlive(buf)
	if err == nil && requestType&0x80 != 0 && n > 0 {
		copy(data, buf[:n])
	}
	return n, err
}

// Bulk does a bulk transfer on endpoint ep (bit 7 set = IN). Returns bytes transferred.
func (d *Device) Bulk(ep uint8, data []byte, timeoutMs uint32) (int, error) {
	buf := make([]byte, len(data)+1)
	copy(buf, data)
	bt := &bulkTransfer{Ep: uint32(ep), Len: uint32(len(data)), Timeout: timeoutMs, Data: unsafe.Pointer(&buf[0])}
	n, err := d.ioctl(ioctlBulk, unsafe.Pointer(bt))
	runtime.KeepAlive(bt)
	runtime.KeepAlive(buf)
	if err == nil && ep&0x80 != 0 && n > 0 {
		copy(data, buf[:n])
	}
	return n, err
}

// Endpoints lists endpoint addresses and types ("Bulk", "Interrupt", ...) of interface 0.
func (d *Device) Endpoints() map[uint8]string {
	out := map[uint8]string{}
	matches, _ := filepath.Glob(filepath.Join(d.SysPath, "*:1.0", "ep_*"))
	for _, m := range matches {
		a, err := readHex(filepath.Join(m, "bEndpointAddress"))
		if err != nil {
			continue
		}
		t, _ := os.ReadFile(filepath.Join(m, "type"))
		out[uint8(a)] = strings.TrimSpace(string(t))
	}
	return out
}

// Close releases claimed interfaces and closes the node.
func (d *Device) Close() error {
	for _, i := range d.claimed {
		v := i
		d.ioctl(ioctlRelease, unsafe.Pointer(&v))
	}
	return d.f.Close()
}
