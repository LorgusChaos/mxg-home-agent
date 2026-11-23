package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mxg/internal/must"
)

// Settings go through the agent: the viewer drops a command file into /data/mxg/cmd over adb and waits for the
// agent's .result.json. The agent re-checks every value against its own whitelist and limits.

var cmdMu sync.Mutex // one command at a time

type settingsFile struct {
	Time   string             `json:"time"`
	Raw    map[string]uint16  `json:"raw"`
	Values map[string]float64 `json:"values"`
}

func loadSettings() (settingsFile, error) {
	var f settingsFile
	b, err := os.ReadFile(filepath.Join(*dataDir, "settings.json"))
	if err != nil {
		return f, err
	}
	return f, json.Unmarshal(b, &f)
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return time.Now().Format("150405") + "-" + hex.EncodeToString(b)
}

// runCommand pushes a command to the box and waits for the agent's result.
func runCommand(cmd map[string]any) (map[string]any, error) {
	cmdMu.Lock()
	defer cmdMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	id := newID()
	cmd["id"] = id
	cmd["expires"] = time.Now().Add(2 * time.Minute).Unix()
	b, _ := json.Marshal(cmd)
	tmp := filepath.Join(os.TempDir(), "mxg-"+id+".json")
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return nil, err
	}
	defer os.Remove(tmp)

	devs, _ := adb(ctx, "devices")
	if !strings.Contains(devs, *box+"\tdevice") {
		adb(ctx, "connect", *box)
	}
	remoteTmp := "/data/mxg/cmd/" + id + ".tmp"
	pushed := false
	for i := 0; i < 5 && !pushed; i++ {
		if _, err := adb(ctx, "-s", *box, "push", tmp, remoteTmp); err == nil {
			if out, err := adb(ctx, "-s", *box, "shell", "mv "+remoteTmp+" /data/mxg/cmd/"+id+".json && echo ok"); err == nil && strings.Contains(out, "ok") {
				pushed = true
			}
		}
		if !pushed {
			time.Sleep(3 * time.Second)
		}
	}
	if !pushed {
		return nil, fmt.Errorf("не удалось передать команду на приставку (связь)")
	}
	res := "/data/mxg/cmd/" + id + ".result.json"
	deadline := time.Now().Add(100 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		out, err := adb(ctx, "-s", *box, "shell", "cat "+res+" 2>/dev/null")
		out = strings.TrimSpace(out)
		if err != nil || !strings.HasPrefix(out, "{") {
			continue
		}
		var r map[string]any
		if json.Unmarshal([]byte(out), &r) != nil {
			continue
		}
		adb(ctx, "-s", *box, "shell", "rm -f "+res)
		// fresh settings.json
		adb(ctx, "-s", *box, "pull", "/data/mxg/settings.json", filepath.Join(*dataDir, "settings.json"))
		return r, nil
	}
	return nil, fmt.Errorf("агент не ответил за 100 с (команда могла выполниться позже — проверьте журнал событий)")
}

func registerSettingsAPI(mux *http.ServeMux) {
	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		f, err := loadSettings()
		resp := map[string]any{"defs": must.Settings}
		if err == nil {
			resp["time"], resp["values"] = f.Time, f.Values
		} else {
			resp["error"] = "настройки ещё не считаны с приставки"
		}
		writeJSON(w, resp)
	})
	mux.HandleFunc("/api/settings/refresh", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST", http.StatusMethodNotAllowed)
			return
		}
		res, err := runCommand(map[string]any{"op": "read_settings"})
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, res)
	})
	mux.HandleFunc("/api/settings/set", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Key   string  `json:"key"`
			Value float64 `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("settings/set: bad request: %v", err)
			writeJSON(w, map[string]any{"ok": false, "error": "bad request"})
			return
		}
		log.Printf("settings/set %s = %v: request", req.Key, req.Value)
		s, ok := must.FindSetting(req.Key)
		if !ok {
			writeJSON(w, map[string]any{"ok": false, "error": "нет такой настройки"})
			return
		}
		if s.Kind != "bit" { // bits need the current register; the agent checks them
			if _, err := s.Encode(req.Value, 0); err != nil {
				writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
				return
			}
		}
		res, err := runCommand(map[string]any{"op": "set", "key": req.Key, "value": req.Value})
		if err != nil {
			log.Printf("settings/set %s = %v: %v", req.Key, req.Value, err)
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		log.Printf("settings/set %s = %v: %v", req.Key, req.Value, res)
		writeJSON(w, res)
	})
}
