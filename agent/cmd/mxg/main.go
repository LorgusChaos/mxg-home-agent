//go:build linux

// mxg is the home agent on the Mini MX-G box: polls the Must inverter, logs to CSV, sends Telegram notifications,
// answers bot commands, keeps adbd and Wi-Fi healthy. Never reboots the box.
//
// Files in -dir (default /data/mxg): config.json, mxg.log, data-*.csv, raw-*.csv.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "golang.org/x/crypto/x509roots/fallback" // Android has no Let's Encrypt/system roots in Go's search paths

	"mxg/internal/jkbms"
	"mxg/internal/must"
	"mxg/internal/rules"
	"mxg/internal/store"
	"mxg/internal/sys"
	"mxg/internal/tg"
)

const version = "0.4.1"

type Config struct {
	TelegramToken string `json:"telegram_token"`
	OwnerChatID   int64  `json:"owner_chat_id"`
	PairCode      string `json:"pair_code"`
	PollSeconds   int    `json:"poll_seconds"`
	BatteryCells  int    `json:"battery_cells"`
}

type agent struct {
	dir     string
	cfgPath string

	mu        sync.Mutex
	cfg       Config
	last      must.Snapshot
	lastErr   string
	muteUntil time.Time
	adbFixes  int
	started   time.Time
	bms       jkbms.CellInfo // last JK BMS reading
	bmsTime   time.Time
	bmsDev    jkbms.DeviceInfo

	engMu sync.Mutex // rules engine is used by the poll loop and by /status
	eng   *rules.Engine
	store *store.Store
	out   *tg.Outbox
	tgc   *tg.Client
}

// ---------- logging ----------

type rotWriter struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func (w *rotWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil || w.size > 1<<20 {
		if w.f != nil {
			w.f.Close()
			os.Rename(w.path, w.path+".1")
		}
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return 0, err
		}
		st, _ := f.Stat()
		w.f, w.size = f, st.Size()
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// ---------- config ----------

func randomCode() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(900000))
	return strconv.FormatInt(n.Int64()+100000, 10)
}

func (a *agent) loadConfig() error {
	cfg := Config{PollSeconds: 10, BatteryCells: 8}
	b, err := os.ReadFile(a.cfgPath)
	if err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return fmt.Errorf("config.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if cfg.PollSeconds < 5 {
		cfg.PollSeconds = 10
	}
	// token.txt (pushed by scripts/set_token.ps1) is imported once and deleted, so the token never travels in a
	// command line or chat
	tokPath := filepath.Join(a.dir, "token.txt")
	if b, err := os.ReadFile(tokPath); err == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			cfg.TelegramToken = tok
			log.Printf("telegram token imported from token.txt")
		}
		os.Remove(tokPath)
	}
	if cfg.PairCode == "" && cfg.OwnerChatID == 0 {
		cfg.PairCode = randomCode()
	}
	a.cfg = cfg
	return a.saveConfig()
}

func (a *agent) saveConfig() error {
	b, _ := json.MarshalIndent(a.cfg, "", "  ")
	tmp := a.cfgPath + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, a.cfgPath)
}

func (a *agent) owner() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.OwnerChatID
}

// ---------- notifications ----------

// notify records every event in events.csv and sends the notifiable ones (unless muted; ⚠️/❗ always go out).
func (a *agent) notify(evs []rules.Event) {
	for _, ev := range evs {
		log.Printf("event %s: %s", ev.Kind, strings.ReplaceAll(ev.Text, "\n", " | "))
		if err := a.store.WriteEvent(ev.Time, ev.Kind, ev.Notify, ev.Text); err != nil {
			log.Printf("events.csv: %v", err)
		}
		if !ev.Notify {
			continue
		}
		a.mu.Lock()
		muted := time.Now().Before(a.muteUntil)
		a.mu.Unlock()
		critical := strings.HasPrefix(ev.Text, "⚠️") || strings.HasPrefix(ev.Text, "❗")
		if muted && !critical {
			continue
		}
		if a.out != nil && !a.out.Post(ev.Text) {
			log.Printf("outbox full, dropped")
		}
	}
}

// ---------- loops ----------

func (a *agent) pollLoop(ctx context.Context) {
	var r must.Reader
	defer r.Close()
	interval := time.Duration(a.cfg.PollSeconds) * time.Second
	var lastRaw time.Time
	firstOK := true
	t := time.NewTicker(interval)
	defer t.Stop()
	// commands (settings) are handled in this goroutine too: it owns the serial port
	cmdTick := time.NewTicker(2 * time.Second)
	defer cmdTick.Stop()
	os.MkdirAll(a.cmdDir(), 0755)
	var lastSettings time.Time
	wait := func() bool {
		for {
			select {
			case <-ctx.Done():
				return false
			case <-cmdTick.C:
				a.processCommands(&r)
			case <-t.C:
				if sys.ClockSane() && time.Since(lastSettings) >= 10*time.Minute {
					if _, err := a.readSettings(&r); err != nil {
						log.Printf("settings: %v", err)
					}
					lastSettings = time.Now()
				}
				return true
			}
		}
	}
	warnedClock := false
	readFails := 0
	var lastUSBReset time.Time
	usbBackoff := time.Minute
	for {
		s, err := r.Read()
		now := time.Now()
		if !sys.ClockSane() {
			// no RTC: do not write 2015-dated rows or fire rules until the clock is set (sysLoop syncs it)
			if !warnedClock {
				log.Printf("clock not set yet (%s): readings not stored", now.Format("2006-01-02 15:04"))
				warnedClock = true
			}
			if !wait() {
				return
			}
			continue
		}
		a.pollBMS(now) // before the inverter rules, so battery levels use the fresh BMS SOC
		if err != nil {
			readFails++
			// the CH340 hangs when mains returns (relay transfer): power-cycle its USB port, never the box
			if readFails >= 3 && now.Sub(lastUSBReset) >= usbBackoff {
				ctrl, rerr := r.ResetUSB()
				lastUSBReset = time.Now()
				msg := fmt.Sprintf("USB инвертора перезапущен (питание порта %s выкл/вкл) после %d ошибок: %v", filepath.Base(ctrl), readFails, err)
				if rerr != nil {
					msg += "; сбой сброса: " + rerr.Error()
				}
				log.Print(msg)
				a.notify([]rules.Event{{Time: now, Kind: "usb_reset", Text: "🔌 " + msg, Notify: false}})
				if usbBackoff < 10*time.Minute {
					usbBackoff = map[time.Duration]time.Duration{time.Minute: 2 * time.Minute,
						2 * time.Minute: 5 * time.Minute, 5 * time.Minute: 10 * time.Minute}[usbBackoff]
				}
			}
		} else {
			readFails, usbBackoff = 0, time.Minute
		}
		if err != nil {
			a.mu.Lock()
			a.lastErr = now.Format("15:04:05 ") + err.Error()
			a.mu.Unlock()
			log.Printf("read: %v", err)
			a.engMu.Lock()
			evs := a.eng.OnError(now, err)
			a.engMu.Unlock()
			a.notify(evs)
		} else {
			a.engMu.Lock()
			msgs := a.eng.OnReading(s)
			soc := a.eng.VoltSOC()
			a.engMu.Unlock()
			if err := a.store.Write(s, soc); err != nil {
				log.Printf("store: %v", err)
			}
			if now.Sub(lastRaw) >= 5*time.Minute {
				if err := a.store.WriteRaw(s); err != nil {
					log.Printf("store raw: %v", err)
				}
				lastRaw = now
			}
			a.mu.Lock()
			a.last = s
			a.lastErr = ""
			a.mu.Unlock()
			if firstOK {
				firstOK = false
				log.Printf("first reading: grid %.0f V, batt %.1f V, load %d W", s.GridV, s.BattV, s.LoadW)
			}
			a.notify(msgs)
		}
		if !wait() {
			return
		}
	}
}

// sysLoop keeps Wi-Fi power save off and unlocks adbd when a client is locked out (never reboots).
func (a *agent) sysLoop(ctx context.Context) {
	prev := 0
	prepared := false // at boot Android services may not be up yet: retry until settings/svc succeed
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for tick := 0; ; tick++ {
		// Android turns Wi-Fi power save back on about every 45 s: check every 5 s (cheap)
		if old, changed := sys.KeepWiFiAwake(); changed && tick%12 == 0 {
			log.Printf("wifi PM was %s -> 0 (logged once a minute)", old)
		}
		if tick%60 == 1 { // every 5 min: keep the BLE bridge app running
			ensureBridge()
		}
		if tick%3 != 0 { // the rest runs every 15 s
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			continue
		}
		if !sys.ClockSane() {
			if srv, err := sys.SyncClock(); err != nil {
				log.Printf("clock sync: %v", err)
			} else {
				log.Printf("clock set from NTP %s: %s", srv, time.Now().Format("2006-01-02 15:04:05"))
			}
		} else if tick%720 == 3 { // hourly (720 × 5 s): the box drifts (seen ~30 s), correct if off by > 2 s
			if off, err := sys.CorrectClock(2 * time.Second); err != nil {
				log.Printf("clock check: %v", err)
			} else if off > 2*time.Second || off < -2*time.Second {
				log.Printf("clock corrected by %s", off.Round(time.Millisecond))
			}
		}
		if !prepared {
			notes := sys.PrepareWiFi()
			for _, n := range notes {
				log.Printf("prepare wifi (will retry): %s", n)
			}
			if len(notes) == 0 {
				prepared = true
				log.Printf("wifi suspend optimizations off, stay-on set")
			}
		}
		n := sys.LockedOutADB()
		if n > 0 && prev > 0 {
			res, err := sys.RestartADBD()
			a.mu.Lock()
			a.adbFixes++
			a.mu.Unlock()
			log.Printf("adbd: %d locked-out clients -> restart: %s %v", n, res, err)
			n = 0
		}
		prev = n
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *agent) botLoop(ctx context.Context) {
	wait := 5 * time.Second
	for ctx.Err() == nil {
		msgs, err := a.tgc.GetUpdates(ctx, 50)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("getUpdates: %v (retry in %s)", err, wait)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if wait < 5*time.Minute {
				wait *= 2
			}
			continue
		}
		wait = 5 * time.Second
		for _, m := range msgs {
			a.handle(ctx, m)
		}
	}
}

// ---------- bot commands ----------

const help = `Команды:
/status — состояние сейчас
/export [N] — CSV за N дней (по умолчанию 1, максимум 31)
/mute [2h|30m|off] — тишина (кроме ⚠️ и ❗)
/adb_restart — перезапустить adbd на приставке`

func (a *agent) reply(ctx context.Context, chat int64, text string) {
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := a.tgc.Send(sctx, chat, text); err != nil {
		log.Printf("reply: %v", err)
	}
}

func (a *agent) handle(ctx context.Context, m tg.Message) {
	fields := strings.Fields(m.Text)
	if len(fields) == 0 {
		return
	}
	cmd := strings.ToLower(strings.SplitN(fields[0], "@", 2)[0])
	args := fields[1:]

	a.mu.Lock()
	owner, code := a.cfg.OwnerChatID, a.cfg.PairCode
	a.mu.Unlock()

	if owner == 0 {
		if cmd == "/pair" && len(args) == 1 && code != "" && args[0] == code {
			a.mu.Lock()
			a.cfg.OwnerChatID = m.ChatID
			a.cfg.PairCode = ""
			err := a.saveConfig()
			a.mu.Unlock()
			log.Printf("paired with chat %d (%s), save: %v", m.ChatID, m.From, err)
			a.reply(ctx, m.ChatID, "✅ Готово, теперь я отвечаю только вам.\n\n"+help)
			return
		}
		log.Printf("ignored message from chat %d (%s): not paired", m.ChatID, m.From)
		return
	}
	if m.ChatID != owner {
		log.Printf("ignored message from foreign chat %d (%s)", m.ChatID, m.From)
		return
	}

	switch cmd {
	case "/start", "/help":
		a.reply(ctx, m.ChatID, help)
	case "/status":
		a.reply(ctx, m.ChatID, a.status())
	case "/mute":
		a.reply(ctx, m.ChatID, a.mute(args))
	case "/export":
		a.export(ctx, m.ChatID, args)
	case "/adb_restart":
		res, err := sys.RestartADBD()
		if err != nil {
			a.reply(ctx, m.ChatID, "adbd: "+res+" ("+err.Error()+")")
			return
		}
		a.reply(ctx, m.ChatID, "adbd перезапущен: "+res)
	default:
		a.reply(ctx, m.ChatID, "Не понял. "+help)
	}
}

func (a *agent) status() string {
	a.mu.Lock()
	s, lastErr, muteUntil, fixes, started := a.last, a.lastErr, a.muteUntil, a.adbFixes, a.started
	a.mu.Unlock()
	var b strings.Builder
	if s.Time.IsZero() {
		b.WriteString("Данных от инвертора ещё нет.\n")
	} else {
		a.engMu.Lock()
		soc := a.eng.VoltSOC()
		known, grid, since := a.eng.GridState()
		ei, wi, ec, wc := a.eng.ActiveFaults()
		a.engMu.Unlock()
		gridTxt := "?"
		stale := time.Since(s.Time) > time.Minute
		if stale {
			fmt.Fprintf(&b, "⚠️ Нет связи с инвертором с %s — его данные ниже устаревшие\n", s.Time.Format("02.01 15:04:05"))
			gridTxt = "неизвестно (нет связи с инвертором)"
		} else if known {
			if grid {
				gridTxt = fmt.Sprintf("есть (%.0f В), с %s", s.GridV, since.Format("02.01 15:04"))
			} else {
				gridTxt = fmt.Sprintf("НЕТ, с %s (%s)", since.Format("02.01 15:04"),
					time.Since(since).Round(time.Minute).String())
			}
		}
		fmt.Fprintf(&b, "⚡ Сеть: %s\n", gridTxt)
		fmt.Fprintf(&b, "⚙️ Режим: %s\n", must.WorkStateName(s.WorkState))
		b.WriteString(a.bmsStatus())
		fmt.Fprintf(&b, "🔋 Инвертор видит батарею: %.1f В (≈%.0f%% по напряжению), %d Вт, %d А\n", s.BattV, soc, s.BattW, s.BattA)
		fmt.Fprintf(&b, "🔌 Нагрузка: %d Вт (%d%%), выход %.0f В %.2f Гц\n", s.LoadW, s.LoadPct, s.OutV, s.OutHz)
		fmt.Fprintf(&b, "☀️ Панели: %.1f В, %d Вт\n", s.PvV, s.PvW)
		fmt.Fprintf(&b, "🌡 радиатор AC %d, трансформатор %d, радиатор DC %d, контроллер %d °C\n",
			s.TempA, s.TempB, s.TempDC, s.ChgTemp)
		ec &^= must.QuietChargerErrors
		if ei|wi|ec|wc == 0 {
			b.WriteString("✅ Ошибок и предупреждений нет\n")
		} else {
			if ei != 0 {
				fmt.Fprintf(&b, "❗ Ошибки инвертора: %s\n", must.Describe(ei, must.InverterErrors))
			}
			if wi != 0 {
				fmt.Fprintf(&b, "⚠️ Предупреждения инвертора: %s\n", must.Describe(wi, must.InverterWarnings))
			}
			if ec != 0 {
				fmt.Fprintf(&b, "❗ Ошибки контроллера: %s\n", must.Describe(ec, must.ChargerErrors))
			}
			if wc != 0 {
				fmt.Fprintf(&b, "⚠️ Предупреждения контроллера: %s\n", must.Describe(wc, must.ChargerWarnings))
			}
		}
		e := s.Energy
		fmt.Fprintf(&b, "Σ за всё время: нагрузка %.1f, из сети %.1f, панели %.1f, разряд батареи %.1f кВт·ч\n",
			e.Load, e.Buy, e.PV, e.Discharger)
		fmt.Fprintf(&b, "Обновлено: %s\n", s.Time.Format("15:04:05"))
	}
	if lastErr != "" {
		fmt.Fprintf(&b, "❗ Последняя ошибка: %s\n", lastErr)
	}
	if time.Now().Before(muteUntil) {
		fmt.Fprintf(&b, "🔕 Тишина до %s\n", muteUntil.Format("15:04"))
	}
	fmt.Fprintf(&b, "Агент %s, работает %s, перезапусков adbd: %d", version,
		time.Since(started).Round(time.Minute), fixes)
	return b.String()
}

func (a *agent) mute(args []string) string {
	d := 2 * time.Hour
	if len(args) > 0 {
		if strings.EqualFold(args[0], "off") {
			a.mu.Lock()
			a.muteUntil = time.Time{}
			a.mu.Unlock()
			return "🔔 Уведомления включены."
		}
		p, err := time.ParseDuration(args[0])
		if err != nil || p <= 0 || p > 48*time.Hour {
			return "Формат: /mute 2h, /mute 30m, /mute off (до 48h)"
		}
		d = p
	}
	until := time.Now().Add(d)
	a.mu.Lock()
	a.muteUntil = until
	a.mu.Unlock()
	return "🔕 Тишина до " + until.Format("15:04") + " (⚠️ и ❗ всё равно придут)."
}

func (a *agent) export(ctx context.Context, chat int64, args []string) {
	days := 1
	if len(args) > 0 {
		n, err := strconv.Atoi(strings.TrimSuffix(strings.ToLower(args[0]), "d"))
		if err != nil || n < 1 || n > 31 {
			a.reply(ctx, chat, "Формат: /export 7 (дней, 1–31)")
			return
		}
		days = n
	}
	path, rows, err := a.store.Export(days, time.Now())
	if err != nil {
		a.reply(ctx, chat, "Ошибка экспорта: "+err.Error())
		return
	}
	defer os.Remove(path)
	sctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := a.tgc.SendDocument(sctx, chat, path, fmt.Sprintf("%d строк за %d дн.", rows, days)); err != nil {
		a.reply(ctx, chat, "Не удалось отправить файл: "+err.Error())
	}
}

// ---------- main ----------

func main() {
	dir := flag.String("dir", "/data/mxg", "data directory")
	flag.Parse()

	if err := os.MkdirAll(*dir, 0755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// log only to the rotating file; stderr (run.sh -> crash.log) then holds just panics
	log.SetOutput(&rotWriter{path: filepath.Join(*dir, "mxg.log")})
	log.SetFlags(log.Ldate | log.Ltime)

	// single instance: abstract unix socket
	lock, err := net.Listen("unix", "@mxg-agent")
	if err != nil {
		log.Printf("another mxg is running (%v), exiting", err)
		os.Exit(0)
	}
	defer lock.Close()

	tz := sys.SetLocalTime()
	a := &agent{dir: *dir, cfgPath: filepath.Join(*dir, "config.json"), started: time.Now(),
		store: &store.Store{Dir: *dir}}
	if err := a.loadConfig(); err != nil {
		log.Fatalf("config: %v", err)
	}
	a.eng = rules.New(rules.Config{Cells: a.cfg.BatteryCells})
	log.Printf("mxg %s start, tz %s, poll %ds, dns %v", version, tz, a.cfg.PollSeconds, sys.DNSServers())

	if n := sys.StopShellWatchdog(); n > 0 {
		log.Printf("stopped %d interim shell watchdog(s)", n)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if a.cfg.TelegramToken != "" {
		tr := &http.Transport{DialContext: sys.DialContext, TLSHandshakeTimeout: 20 * time.Second,
			IdleConnTimeout: 90 * time.Second, MaxIdleConns: 4}
		a.tgc = &tg.Client{Token: a.cfg.TelegramToken, HTTP: &http.Client{Transport: tr, Timeout: 70 * time.Second},
			Log: log.Printf}
		a.out = tg.NewOutbox(ctx, a.tgc, a.owner, 200)
		if a.owner() == 0 {
			log.Printf("not paired: send /pair %s to the bot", a.cfg.PairCode)
		} else {
			a.out.Post("🟢 Агент " + version + " запущен.")
		}
		go a.botLoop(ctx)
	} else {
		log.Printf("no telegram_token in config.json: running without the bot")
	}

	go a.sysLoop(ctx)
	a.pollLoop(ctx)
	log.Printf("mxg stop")
}
