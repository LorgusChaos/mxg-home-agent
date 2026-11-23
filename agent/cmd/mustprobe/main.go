//go:build linux

// mustprobe finds out which protocol the Must inverter speaks on its CH340 USB-serial port.
// Read-only: it sends only query commands (Voltronic Q* and Modbus read functions 3/4), never settings.
//
//	mustprobe -mode scan                        try Voltronic @2400 and Modbus @19200/9600 (default)
//	mustprobe -mode volt -baud 2400 -cmds QPIGS,QMOD
//	mustprobe -mode modbus -baud 19200 -slave 4 -fn 3 -reg 25201 -n 20
//	mustprobe -mode listen -baud 2400 -secs 5   dump anything the inverter sends by itself
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"mxg/internal/ch340"
	"mxg/internal/proto"
)

func dump(label string, b []byte) {
	var a strings.Builder
	for _, c := range b {
		if c >= 0x20 && c < 0x7f {
			a.WriteByte(c)
		} else {
			a.WriteByte('.')
		}
	}
	fmt.Printf("  %s %d bytes\n    hex: % x\n    asc: %s\n", label, len(b), b, a.String())
}

func volt(p *ch340.Port, cmd string) bool {
	p.Drain()
	f := proto.VoltronicFrame(cmd)
	fmt.Printf("> volt %s @%d  (% x)\n", cmd, p.Baud, f)
	if err := p.Write(f); err != nil {
		fmt.Println("  write error:", err)
		return false
	}
	resp, err := p.ReadUntil(3*time.Second, 400*time.Millisecond, func(b []byte) bool { return b[len(b)-1] == '\r' })
	if err != nil {
		fmt.Println("  read error:", err)
	}
	if len(resp) == 0 {
		fmt.Println("  no reply")
		return false
	}
	dump("reply", resp)
	if payload, ok, err := proto.VoltronicParse(resp); err == nil {
		fmt.Printf("    parsed: %q crcOK=%v\n", payload, ok)
		return ok
	}
	return false
}

func modbus(p *ch340.Port, slave, fn uint8, reg, n uint16) bool {
	p.Drain()
	f := proto.ModbusRead(slave, fn, reg, n)
	fmt.Printf("> modbus slave=%d fn=%d reg=%d n=%d @%d  (% x)\n", slave, fn, reg, n, p.Baud, f)
	if err := p.Write(f); err != nil {
		fmt.Println("  write error:", err)
		return false
	}
	resp, err := p.ReadUntil(1500*time.Millisecond, 200*time.Millisecond, func(b []byte) bool {
		l := proto.ModbusResponseLen(b)
		return l > 0 && len(b) >= l
	})
	if err != nil {
		fmt.Println("  read error:", err)
	}
	if len(resp) == 0 {
		fmt.Println("  no reply")
		return false
	}
	dump("reply", resp)
	regs, exc, err := proto.ModbusParse(resp)
	switch {
	case err != nil:
		fmt.Println("    parse:", err)
		return false
	case exc != 0:
		fmt.Printf("    modbus exception %d (device answers Modbus, register/function not supported)\n", exc)
		return true
	default:
		fmt.Printf("    regs: %v\n", regs)
		return true
	}
}

func listen(p *ch340.Port, secs int) {
	fmt.Printf("> listen %ds @%d\n", secs, p.Baud)
	b, err := p.ReadUntil(time.Duration(secs)*time.Second, time.Duration(secs)*time.Second, nil)
	if err != nil {
		fmt.Println("  read error:", err)
	}
	if len(b) == 0 {
		fmt.Println("  nothing")
		return
	}
	dump("got", b)
}

func main() {
	mode := flag.String("mode", "scan", "scan | volt | modbus | listen")
	baud := flag.Int("baud", 2400, "baud rate")
	cmds := flag.String("cmds", "QPI,QPIGS,QMOD,QPIWS,QPIRI,QID,QVFW", "Voltronic commands (comma separated)")
	slave := flag.Int("slave", 4, "Modbus slave id")
	fn := flag.Int("fn", 3, "Modbus function: 3 holding, 4 input")
	reg := flag.Int("reg", 25201, "Modbus start register")
	n := flag.Int("n", 10, "Modbus register count")
	secs := flag.Int("secs", 5, "listen seconds")
	ranges := flag.String("ranges", "0-499,10000-10199,15200-15399,20000-20399,25200-25499,30000-30199,40000-40199,50000-50199,60000-60199",
		"sweep: register ranges (from-to, comma separated); block size = -n")
	flag.Parse()

	p, err := ch340.Open(*baud)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open CH340:", err)
		os.Exit(1)
	}
	defer p.Close()
	fmt.Printf("CH340 open, chip version 0x%02x, baud %d\n", p.Version, p.Baud)

	switch *mode {
	case "volt":
		for _, c := range strings.Split(*cmds, ",") {
			volt(p, strings.TrimSpace(c))
		}
	case "modbus":
		modbus(p, uint8(*slave), uint8(*fn), uint16(*reg), uint16(*n))
	case "listen":
		listen(p, *secs)
	case "scan":
		scan(p)
	case "sweep":
		sweep(p, uint8(*slave), *ranges, *n)
	default:
		fmt.Fprintln(os.Stderr, "unknown mode", *mode)
		os.Exit(2)
	}
}

// quiet single read for sweep: returns regs, exception code, or error
func readQuiet(p *ch340.Port, slave uint8, reg, n uint16) ([]uint16, uint8, error) {
	p.Drain()
	if err := p.Write(proto.ModbusRead(slave, 3, reg, n)); err != nil {
		return nil, 0, err
	}
	resp, err := p.ReadUntil(1500*time.Millisecond, 200*time.Millisecond, func(b []byte) bool {
		l := proto.ModbusResponseLen(b)
		return l > 0 && len(b) >= l
	})
	if err != nil {
		return nil, 0, err
	}
	if len(resp) == 0 {
		return nil, 0, fmt.Errorf("no reply")
	}
	return proto.ModbusParse(resp)
}

// sweep reads register ranges block by block (function 3 only, read-only) and prints blocks that hold data.
func sweep(p *ch340.Port, slave uint8, ranges string, block int) {
	if block <= 0 || block > 100 {
		block = 50
	}
	if err := p.SetBaud(19200); err != nil {
		fmt.Println("set baud:", err)
		return
	}
	for _, rg := range strings.Split(ranges, ",") {
		var from, to int
		if _, err := fmt.Sscanf(strings.TrimSpace(rg), "%d-%d", &from, &to); err != nil || to < from {
			fmt.Println("bad range", rg)
			continue
		}
		fmt.Printf("=== %d-%d ===\n", from, to)
		for a := from; a <= to; a += block {
			n := block
			if a+n-1 > to {
				n = to - a + 1
			}
			regs, exc, err := readQuiet(p, slave, uint16(a), uint16(n))
			switch {
			case err != nil:
				fmt.Printf("%5d+%-3d %v\n", a, n, err)
			case exc != 0:
				fmt.Printf("%5d+%-3d exception %d\n", a, n, exc)
			default:
				nz := false
				for _, v := range regs {
					if v != 0 {
						nz = true
						break
					}
				}
				if !nz {
					fmt.Printf("%5d+%-3d all zero\n", a, n)
				} else {
					fmt.Printf("%5d+%-3d %v\n", a, n, regs)
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func setBaud(p *ch340.Port, b int) bool {
	if err := p.SetBaud(b); err != nil {
		fmt.Println("set baud:", err)
		return false
	}
	time.Sleep(100 * time.Millisecond)
	return true
}

func scan(p *ch340.Port) {
	found := []string{}
	for _, b := range []int{2400, 9600} {
		fmt.Printf("\n=== Voltronic @%d ===\n", b)
		if !setBaud(p, b) {
			continue
		}
		listen(p, 2)
		for _, c := range []string{"QPI", "QPIGS", "QMOD"} {
			if volt(p, c) {
				found = append(found, fmt.Sprintf("Voltronic @%d %s", b, c))
			}
		}
	}
	for _, b := range []int{19200, 9600} {
		fmt.Printf("\n=== Modbus RTU @%d ===\n", b)
		if !setBaud(p, b) {
			continue
		}
		listen(p, 2)
		for _, s := range []uint8{4, 1} {
			for _, r := range []uint16{25201, 15201, 10001, 0} {
				if modbus(p, s, 3, r, 10) {
					found = append(found, fmt.Sprintf("Modbus @%d slave %d reg %d", b, s, r))
				}
			}
		}
	}
	fmt.Println("\n=== result ===")
	if len(found) == 0 {
		fmt.Println("no protocol answered")
	}
	for _, f := range found {
		fmt.Println("answered:", f)
	}
}
