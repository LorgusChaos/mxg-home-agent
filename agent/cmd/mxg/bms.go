//go:build linux

package main

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"mxg/internal/jkbms"
	"mxg/internal/rules"
)

// The JK BMS is read over BLE by the Android bridge app com.mxg.bmsbridge (Android BLE API only: the box's
// Bluetooth runs under Bluedroid, no kernel HCI). The app writes raw frames to its files dir; the agent decodes.

var lastBMSFrame time.Time

// ensureBridge (re)starts the bridge service; idempotent.
func ensureBridge() {
	out, err := exec.Command("/system/bin/sh", "/system/bin/am", "startservice", "-n", "com.mxg.bmsbridge/.BridgeService").CombinedOutput()
	if err != nil || strings.Contains(string(out), "Error") {
		log.Printf("bms bridge start: %v %s", err, strings.TrimSpace(string(out)))
	}
}

// pollBMS reads the latest cell-info frame (called from the poll goroutine).
func (a *agent) pollBMS(now time.Time) {
	f, t, err := jkbms.ReadFrame(jkbms.BridgeDir, 0x02)
	fresh := err == nil && now.Sub(t) < 60*time.Second && t.After(lastBMSFrame)
	if !fresh {
		a.engMu.Lock()
		evs := a.eng.OnBMSMissing(now)
		a.engMu.Unlock()
		a.notify(evs)
		return
	}
	lastBMSFrame = t
	c, err := jkbms.DecodeCellInfo(f)
	if err != nil {
		log.Printf("bms decode: %v", err)
		return
	}
	if err := a.store.WriteBMS(now, c); err != nil {
		log.Printf("store bms: %v", err)
	}
	a.mu.Lock()
	first := a.bmsTime.IsZero()
	a.bms, a.bmsTime = c, now
	a.mu.Unlock()
	if first {
		if df, _, err := jkbms.ReadFrame(jkbms.BridgeDir, 0x03); err == nil {
			if d, err := jkbms.DecodeDeviceInfo(df); err == nil {
				a.mu.Lock()
				a.bmsDev = d
				a.mu.Unlock()
				log.Printf("bms: %s hw %s sw %s serial %s", d.Model, d.HW, d.SW, d.Serial)
			}
		}
		log.Printf("bms first reading: SOC %d%%, %.2f V, %.1f A, cells %.3f..%.3f", c.SOC, c.Voltage, c.Current, c.CellMin, c.CellMax)
	}
	a.engMu.Lock()
	evs := a.eng.OnBMS(rules.BMSReading{Time: now, SOC: c.SOC, CellMin: c.CellMin, CellMax: c.CellMax,
		CellMinIdx: c.CellMinIdx, CellMaxIdx: c.CellMaxIdx, CellDelta: c.CellDelta, Errors: c.Errors,
		ChargeOn: c.ChargeOn, DischargeOn: c.DischargeOn, Current: c.Current}, jkbms.DescribeErrors, jkbms.QuietErrors)
	a.engMu.Unlock()
	a.notify(evs)
}

// bmsStatus renders the BMS part of /status.
func (a *agent) bmsStatus() string {
	a.mu.Lock()
	c, t := a.bms, a.bmsTime
	a.mu.Unlock()
	if t.IsZero() {
		return "🔋 BMS: данных нет\n"
	}
	age := ""
	if time.Since(t) > time.Minute {
		age = fmt.Sprintf(" (данные от %s)", t.Format("15:04"))
	}
	bal := ""
	if c.Balancing != 0 {
		bal = fmt.Sprintf(", балансировка %.2f А", c.BalanceA)
	}
	errs := ""
	if m := c.Errors &^ jkbms.QuietErrors; m != 0 {
		errs = "\n❗ BMS: " + jkbms.DescribeErrors(m)
	}
	return fmt.Sprintf("🔋 BMS: %d%%, %.0f/%.0f А·ч, %+.1f А (%+.0f Вт), %.2f В%s\n"+
		"   ячейки %.3f–%.3f В (Δ %.0f мВ)%s, %.0f/%.0f °C, MOS %.0f °C, циклов %d, SOH %d%%%s\n",
		c.SOC, c.RemainingAh, c.CapacityAh, c.Current, c.Power, c.Voltage, age,
		c.CellMin, c.CellMax, c.CellDelta*1000, bal, c.Temp1, c.Temp2, c.TempMOS, c.Cycles, c.SOH, errs)
}
