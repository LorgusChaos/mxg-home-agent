//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mxg/internal/must"
	"mxg/internal/rules"
)

// Commands arrive as files /data/mxg/cmd/<id>.json (pushed by mxgview over adb) and are answered with
// /data/mxg/cmd/<id>.result.json. Only settings from must.Settings can be written, with their limits.
type command struct {
	ID      string  `json:"id"`
	Op      string  `json:"op"` // read_settings | set
	Key     string  `json:"key,omitempty"`
	Value   float64 `json:"value,omitempty"`
	Expires int64   `json:"expires"` // unix seconds; stale commands are refused
}

type cmdResult struct {
	ID     string  `json:"id"`
	OK     bool    `json:"ok"`
	Error  string  `json:"error,omitempty"`
	Key    string  `json:"key,omitempty"`
	Before float64 `json:"before,omitempty"`
	After  float64 `json:"after,omitempty"`
	Text   string  `json:"text,omitempty"`
	Time   string  `json:"time"`
}

type settingsFile struct {
	Time   string             `json:"time"`
	Raw    map[string]uint16  `json:"raw"`
	Values map[string]float64 `json:"values"`
}

func (a *agent) cmdDir() string { return filepath.Join(a.dir, "cmd") }

// readSettings reads all setting blocks and writes settings.json.
func (a *agent) readSettings(r *must.Reader) (map[uint16]uint16, error) {
	raw := map[uint16]uint16{}
	for _, rg := range must.SettingRanges {
		regs, err := r.ReadBlock(rg[0], rg[1])
		if err != nil {
			return nil, err
		}
		for i, v := range regs {
			raw[rg[0]+uint16(i)] = v
		}
	}
	f := settingsFile{Time: time.Now().Format("2006-01-02 15:04:05"), Raw: map[string]uint16{}, Values: map[string]float64{}}
	for reg, v := range raw {
		f.Raw[strconv.Itoa(int(reg))] = v
	}
	for _, s := range must.Settings {
		f.Values[s.Key] = s.Display(raw[s.Reg])
	}
	b, _ := json.MarshalIndent(f, "", " ")
	tmp := filepath.Join(a.dir, "settings.json.tmp")
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return raw, err
	}
	return raw, os.Rename(tmp, filepath.Join(a.dir, "settings.json"))
}

func (a *agent) writeResult(res cmdResult) {
	res.Time = time.Now().Format("2006-01-02 15:04:05")
	b, _ := json.Marshal(res)
	name := filepath.Join(a.cmdDir(), res.ID+".result.json")
	os.WriteFile(name+".tmp", b, 0644)
	os.Rename(name+".tmp", name)
}

var safeID = func(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// processCommands runs queued commands (called from the poll goroutine, which owns the port).
func (a *agent) processCommands(r *must.Reader) {
	files, _ := filepath.Glob(filepath.Join(a.cmdDir(), "*.json"))
	for _, f := range files {
		base := filepath.Base(f)
		if strings.HasSuffix(base, ".result.json") {
			if st, err := os.Stat(f); err == nil && time.Since(st.ModTime()) > time.Hour {
				os.Remove(f) // results nobody collected
			}
			continue
		}
		b, err := os.ReadFile(f)
		os.Remove(f)
		if err != nil {
			continue
		}
		var c command
		id := strings.TrimSuffix(base, ".json")
		if err := json.Unmarshal(b, &c); err != nil || !safeID(id) {
			log.Printf("cmd %s: bad command file", base)
			continue
		}
		c.ID = id
		a.runCommand(r, c)
	}
}

func (a *agent) runCommand(r *must.Reader, c command) {
	res := cmdResult{ID: c.ID, Key: c.Key}
	if c.Expires != 0 && time.Now().Unix() > c.Expires {
		res.Error = "команда устарела (связь с приставкой была долго недоступна), не выполнена"
		a.writeResult(res)
		log.Printf("cmd %s: expired", c.ID)
		return
	}
	switch c.Op {
	case "read_settings":
		if _, err := a.readSettings(r); err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
		}
	case "set":
		res = a.setSetting(r, c)
	default:
		res.Error = "неизвестная операция " + c.Op
	}
	log.Printf("cmd %s %s %s=%v: ok=%v %s", c.ID, c.Op, c.Key, c.Value, res.OK, res.Error)
	a.writeResult(res)
}

func (a *agent) setSetting(r *must.Reader, c command) cmdResult {
	res := cmdResult{ID: c.ID, Key: c.Key}
	s, ok := must.FindSetting(c.Key)
	if !ok {
		res.Error = "настройки нет в белом списке: " + c.Key
		return res
	}
	cur, err := r.ReadBlock(s.Reg, 1)
	if err != nil {
		res.Error = "не удалось прочитать текущее значение: " + err.Error()
		return res
	}
	res.Before = s.Display(cur[0])
	raw, err := s.Encode(c.Value, cur[0])
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if raw == cur[0] {
		res.OK, res.After, res.Text = true, res.Before, "значение уже такое, запись не нужна"
		return res
	}
	back, err := r.WriteReg(s.Reg, raw)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.After = s.Display(back)
	if back != raw {
		res.Error = fmt.Sprintf("инвертор принял запись, но вернул %s вместо %s", s.Format(res.After), s.Format(c.Value))
	} else {
		res.OK = true
	}
	text := fmt.Sprintf("⚙️ Настройка «%s»: %s → %s", s.Name, s.Format(res.Before), s.Format(res.After))
	if !res.OK {
		text += " (" + res.Error + ")"
	}
	res.Text = text
	a.notify([]rules.Event{{Time: time.Now(), Kind: "setting", Text: text, Notify: true}})
	a.readSettings(r)
	return res
}
