//go:build linux

// Package sys holds the Android-specific glue: properties, DNS, time zone, Wi-Fi power save, adbd watchdog.
package sys

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata" // Android has no /etc/localtime or zoneinfo in Go's format
)

// Getprop reads an Android system property.
func Getprop(name string) string {
	out, err := exec.Command("/system/bin/getprop", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// SetLocalTime sets time.Local from Android's persist.sys.timezone (set it on the box; fallback UTC).
func SetLocalTime() string {
	name := Getprop("persist.sys.timezone")
	if name == "" {
		name = "UTC"
	}
	if loc, err := time.LoadLocation(name); err == nil {
		time.Local = loc
	}
	return name
}

// DNSServers returns IPv4 DNS servers from Android properties, with public fallbacks.
func DNSServers() []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range []string{"net.dns1", "net.dns2", "net.dns3", "net.dns4", "dhcp.wlan0.dns1", "dhcp.wlan0.dns2"} {
		v := Getprop(p)
		if ip := net.ParseIP(v); ip != nil && ip.To4() != nil && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, v := range []string{"1.1.1.1", "8.8.8.8"} {
		if !seen[v] {
			out = append(out, v)
		}
	}
	return out
}

// knownIPs are used when DNS fails (lossy Wi-Fi next to the inverter). TLS still verifies the host name.
var knownIPs = map[string][]string{
	"api.telegram.org": {"149.154.166.110", "149.154.167.220"},
}

// lastGood remembers the last IP that worked per host.
var lastGood sync.Map

// DialContext dials via Dialer(); if name resolution fails it tries the last good IP and then known IPs.
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d := Dialer()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return d.DialContext(ctx, network, addr)
	}
	c, err := d.DialContext(ctx, network, addr)
	if err == nil {
		if ra, ok := c.RemoteAddr().(*net.TCPAddr); ok {
			lastGood.Store(host, ra.IP.String())
		}
		return c, nil
	}
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) {
		return nil, err
	}
	var cands []string
	if v, ok := lastGood.Load(host); ok {
		cands = append(cands, v.(string))
	}
	cands = append(cands, knownIPs[host]...)
	for _, ip := range cands {
		c, err2 := d.DialContext(ctx, network, net.JoinHostPort(ip, port))
		if err2 == nil {
			return c, nil
		}
	}
	return nil, err
}

// Dialer returns a net.Dialer whose resolver queries the Android DNS servers (there is no /etc/resolv.conf).
func Dialer() *net.Dialer {
	var n atomic.Uint32
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			servers := DNSServers()
			s := servers[int(n.Add(1))%len(servers)]
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, network, net.JoinHostPort(s, "53"))
		},
	}
	return &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second, Resolver: r}
}

func run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// PrepareWiFi applies the settings that stop the headless box from dropping incoming packets.
func PrepareWiFi() []string {
	// settings and svc are shell scripts without a #! line: run them through sh
	var notes []string
	if out, err := run("/system/bin/sh", "/system/bin/settings", "put", "global", "wifi_suspend_optimizations_enabled", "0"); err != nil {
		notes = append(notes, "wifi_suspend_optimizations: "+err.Error()+" "+out)
	}
	if out, err := run("/system/bin/sh", "/system/bin/svc", "power", "stayon", "true"); err != nil {
		notes = append(notes, "svc power stayon: "+err.Error()+" "+out)
	}
	return notes
}

// KeepWiFiAwake turns Broadcom power save off if Android turned it back on. Returns the old value if changed.
func KeepWiFiAwake() (old string, changed bool) {
	v, err := run("/system/bin/wl", "PM")
	if err != nil || v == "" || v == "0" {
		return v, false
	}
	run("/system/bin/wl", "PM", "0")
	return v, true
}

// pids of processes whose cmdline (NULs as spaces, trimmed) equals want.
func pidsByCmdline(want string) []int {
	var out []int
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	for _, d := range dirs {
		b, err := os.ReadFile(d + "/cmdline")
		if err != nil || len(b) == 0 {
			continue
		}
		cl := strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
		if cl == want || strings.HasPrefix(cl, want+" ") {
			if pid, err := strconv.Atoi(filepath.Base(d)); err == nil {
				out = append(out, pid)
			}
		}
	}
	return out
}

// StopShellWatchdog kills the interim shell watchdog (the agent takes over its job).
func StopShellWatchdog() int {
	n := 0
	for _, pid := range pidsByCmdline("sh /data/local/tmp/adbd_watchdog.sh") {
		if syscall.Kill(pid, syscall.SIGTERM) == nil {
			n++
		}
	}
	return n
}

// LockedOutADB counts sockets on local port 5555 holding unread data in ESTABLISHED/CLOSE_WAIT:
// clients that adbd accepted but never serves because it still holds a dead session.
func LockedOutADB() int {
	n := 0
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) < 5 || !strings.HasSuffix(fs[1], ":15B3") {
				continue
			}
			if fs[3] != "01" && fs[3] != "08" {
				continue
			}
			if q := strings.SplitN(fs[4], ":", 2); len(q) == 2 && q[1] != "00000000" {
				n++
			}
		}
		fh.Close()
	}
	return n
}

// RestartADBD kills adbd; init respawns it. Falls back to "start adbd". Never reboots.
func RestartADBD() (string, error) {
	for _, pid := range pidsByCmdline("/sbin/adbd") {
		syscall.Kill(pid, syscall.SIGKILL)
	}
	time.Sleep(3 * time.Second)
	if p := pidsByCmdline("/sbin/adbd"); len(p) > 0 {
		return "respawned pid " + strconv.Itoa(p[0]), nil
	}
	if out, err := run("/system/bin/start", "adbd"); err != nil {
		return out, err
	}
	time.Sleep(3 * time.Second)
	if p := pidsByCmdline("/sbin/adbd"); len(p) > 0 {
		return "started pid " + strconv.Itoa(p[0]), nil
	}
	return "adbd not running", os.ErrNotExist
}
