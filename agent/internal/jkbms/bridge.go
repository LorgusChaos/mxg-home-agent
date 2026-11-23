package jkbms

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// BridgeDir is where the Android bridge app (com.mxg.bmsbridge) writes frames; the agent reads it as root.
const BridgeDir = "/data/data/com.mxg.bmsbridge/files"

// ReadFrame reads files/frame_TT.hex ("unix_ms\nHEX\n") and returns the checked frame and its time.
func ReadFrame(dir string, typ byte) ([]byte, time.Time, error) {
	b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("frame_%02x.hex", typ)))
	if err != nil {
		return nil, time.Time{}, err
	}
	lines := strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)
	if len(lines) != 2 {
		return nil, time.Time{}, fmt.Errorf("bad frame file")
	}
	ms, err := strconv.ParseInt(strings.TrimSpace(lines[0]), 10, 64)
	if err != nil {
		return nil, time.Time{}, err
	}
	f, err := ParseHex(lines[1])
	if err != nil {
		return nil, time.Time{}, err
	}
	return f, time.UnixMilli(ms), nil
}
