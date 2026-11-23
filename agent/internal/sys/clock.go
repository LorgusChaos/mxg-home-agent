//go:build linux

package sys

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"syscall"
	"time"
)

// The box has no RTC: after a power cut it boots at 2015-01-01 until Android's NTP catches up.
var saneAfter = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// ClockSane reports whether the system clock looks right.
func ClockSane() bool { return time.Now().After(saneAfter) }

var ntpHosts = []string{"time.google.com", "pool.ntp.org", "time.cloudflare.com"}
var ntpIPs = []string{"216.239.35.0", "216.239.35.4", "162.159.200.1", "162.159.200.123"}

// sntp asks one server and returns its time.
func sntp(addr string) (time.Time, error) {
	c, err := net.DialTimeout("udp4", net.JoinHostPort(addr, "123"), 5*time.Second)
	if err != nil {
		return time.Time{}, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	req := make([]byte, 48)
	req[0] = 0x23 // LI 0, version 4, mode 3 (client)
	if _, err := c.Write(req); err != nil {
		return time.Time{}, err
	}
	resp := make([]byte, 48)
	n, err := c.Read(resp)
	if err != nil {
		return time.Time{}, err
	}
	if n < 48 || resp[0]&0x07 != 4 { // mode 4 = server
		return time.Time{}, fmt.Errorf("bad ntp reply")
	}
	secs := binary.BigEndian.Uint32(resp[40:])
	frac := binary.BigEndian.Uint32(resp[44:])
	if secs == 0 {
		return time.Time{}, fmt.Errorf("ntp: zero time")
	}
	t := time.Unix(int64(secs)-2208988800, int64(uint64(frac)*1e9>>32))
	if !t.After(saneAfter) {
		return time.Time{}, fmt.Errorf("ntp: implausible time %s", t)
	}
	return t, nil
}

// CorrectClock queries NTP and steps the clock only if it is off by more than maxDrift. Returns the offset found.
func CorrectClock(maxDrift time.Duration) (time.Duration, error) {
	var lastErr error
	for _, ip := range ntpIPs {
		t, err := sntp(ip)
		if err != nil {
			lastErr = err
			continue
		}
		off := time.Until(t)
		if off > maxDrift || off < -maxDrift {
			tv := syscall.NsecToTimeval(t.UnixNano())
			if err := syscall.Settimeofday(&tv); err != nil {
				return off, err
			}
		}
		return off, nil
	}
	return 0, lastErr
}

// SyncClock sets the system clock from NTP. Returns the server used.
func SyncClock() (string, error) {
	var cands []string
	r := Dialer().Resolver
	for _, h := range ntpHosts {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		ips, err := r.LookupIP(ctx, "ip4", h)
		cancel()
		if err == nil && len(ips) > 0 {
			cands = append(cands, ips[0].String())
		}
	}
	cands = append(cands, ntpIPs...)
	var lastErr error
	for _, ip := range cands {
		t, err := sntp(ip)
		if err != nil {
			lastErr = err
			continue
		}
		tv := syscall.NsecToTimeval(t.UnixNano())
		if err := syscall.Settimeofday(&tv); err != nil {
			return ip, fmt.Errorf("settimeofday: %w", err)
		}
		return ip, nil
	}
	return "", lastErr
}
