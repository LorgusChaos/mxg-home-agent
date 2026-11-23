# mxg-home-agent

Monitoring and control for a **Must PV1800-series** off-grid inverter and a **JK BMS** lithium battery, running on an
old Android TV box. Built as a home backup-power (UPS) monitor: frequent grid outages, no solar panels required.

[Русская версия](README.ru.md) · Installation: [INSTALL.md](INSTALL.md) (Russian) · Usage: [USAGE.md](USAGE.md)
(Russian) · Design notes: [docs/DECISIONS.md](docs/DECISIONS.md)

```
Inverter ──USB (CH340, Modbus RTU)──┐
                                    ├── TV box: agent "mxg" (Go) ──Wi-Fi──► Telegram bot (alerts, /status)
JK BMS ────Bluetooth LE─────────────┘   (Android bridge app)      └──ADB──► PC viewer (charts, tables, settings)
```

## Features

- **Agent on the box** (single static Go binary, runs as root, autostart, never reboots the box):
  - polls the inverter every 10 s over **Modbus RTU** through a **userspace CH340 driver** (usbfs; the box kernel has no
    `ch341` module);
  - reads the **JK BMS over BLE** via a tiny Android bridge app (real SOC, current, every cell voltage, temperatures,
    alarms, MOSFET states);
  - logs everything to daily CSV files and its own event log;
  - **Telegram bot**: event-only notifications (grid lost/restored with duration, battery 10 % steps, inverter
    faults/warnings, BMS alarms, cell imbalance, link loss) and commands `/status`, `/export`, `/mute`, `/adb_restart`;
  - self-healing: adbd lock-up watchdog, Wi-Fi power-save keeper, NTP clock (no RTC), **USB port power-cycle when the
    CH340 hangs** after a mains transfer.
- **PC viewer** (`mxgview`, Windows): copies data from the box over ADB and serves a local page with charts (battery,
    cells, power, grid, temperatures, outage bands), tables, daily summary, event log, **inverter settings editor**
    (whitelisted, range-checked, read back, notified) and **export for AI analysis** (compact zip + context guide).

## Tested hardware

| Part | Model | Notes |
|---|---|---|
| Box | Mini MX-G, Amlogic S905, Android 5.1.1, 64-bit kernel 3.14, 32-bit userland | root via SuperSU 2.x (`su.d` autostart), ADB over TCP |
| Inverter | Must PV18-3224 VPM II (PV1800 map) | USB port = CH340 `1a86:7523`, Modbus RTU 19200 8N1, slave 4 |
| BMS | JK-B2A8S20P, hw 19H, sw 19.27, 8S LiFePO4 | BLE protocol JK02_32S |

Other Amlogic/Android boxes with root, other PV1800-family Must inverters and JK BMS units with the JK02 protocol
should work with small changes — see "Adapting" in [INSTALL.md](INSTALL.md).

## Repository layout

| Path | Contents |
|---|---|
| `agent/cmd/mxg` | the agent |
| `agent/cmd/mxgview` | PC viewer (embedded web page) |
| `agent/cmd/mustprobe` | inverter probe: protocol scan, Modbus reads, register sweep |
| `agent/internal/` | `usbfs`, `ch340` (driver), `proto` (Modbus RTU), `must` (register map, faults, settings whitelist), `jkbms` (BMS frames), `rules` (notifications, tested), `store`, `tg` (Telegram), `sys` (Android glue), `soc` |
| `agent/deploy/` | scripts installed on the box: supervisor, start/stop, on-box updater, `su.d` autostart |
| `bmsbridge/` | Android BLE bridge app (Java, built without Gradle) |
| `scripts/` | build, deploy, Telegram token setup, adbd watchdog |
| `docs/` | Modbus map, inverter manual notes, design decisions, AI analysis guide |

## Safety

The agent writes to the inverter **only** settings from an explicit whitelist with conservative limits (e.g. battery
voltages ≤ 29.2 V for 8S LiFePO4), verifies them by reading back and announces every change. Calibration, counter
reset and grid-export settings are not exposed. The agent never reboots the box; it restarts only failed services.

## Status

Works in daily use. Planned: feeding BMS data to the inverter over CAN (Pylontech-style emulation via a USB-CAN
adapter). Documentation is in Russian; code comments are in English.

## Credits

- Must PV1800 register map: [mukaschultze/ha-must-inverter](https://github.com/mukaschultze/ha-must-inverter) (MIT)
- JK BMS BLE protocol: [syssi/esphome-jk-bms](https://github.com/syssi/esphome-jk-bms)
- CH340 initialization and baud divisor: Linux `drivers/usb/serial/ch341.c`

## License

[MIT](LICENSE)
