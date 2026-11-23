//go:build linux

package usbfs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// On this box every return of mains power (inverter relay transfer) can hang the inverter's CH340: control
// transfers fail with EPROTO (-71) and even the device descriptor cannot be re-read. Only cutting the port
// power helps. The Amlogic dwc_otg controller exposes it as /sys/devices/dwc2_X/buspower.

// ControllerOf returns the dwc_otg controller dir for a device sysfs path (…/dwc2_b/usb1/1-1 -> …/dwc2_b).
func ControllerOf(devSys string) (string, error) {
	real, err := filepath.EvalSymlinks(devSys)
	if err != nil {
		return "", err
	}
	for dir := real; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "buspower")); err == nil {
			return dir, nil
		}
	}
	return "", fmt.Errorf("no buspower control above %s", real)
}

// PowerCycle switches the port power off for `off` and back on.
func PowerCycle(controller string, off time.Duration) error {
	p := filepath.Join(controller, "buspower")
	if err := os.WriteFile(p, []byte("0"), 0644); err != nil {
		return err
	}
	time.Sleep(off)
	if err := os.WriteFile(p, []byte("1"), 0644); err != nil {
		return err
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "0x1") {
		return fmt.Errorf("buspower after cycle: %s", strings.TrimSpace(string(b)))
	}
	return nil
}
