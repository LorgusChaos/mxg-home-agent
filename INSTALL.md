# Установка

Пошагово: от «голой» приставки до работающего агента, бота, моста BMS и просмотрщика. Команды для ПК даны для
Windows (PowerShell и Git Bash). `<BOX>` — адрес приставки в виде `IP:5555`.

## 0. Что понадобится

**Железо**
- Android ТВ-приставка с **root** (проверено: Amlogic S905, Android 5.1, SuperSU 2.x) и Wi-Fi рядом с инвертором.
  Нужны: свободный USB-A, Bluetooth LE (для BMS), `busybox`.
- Инвертор Must семейства **PV1800** (проверено: PV18-3224 VPM II) — его USB-порт и кабель USB A–B.
- JK BMS с BLE-протоколом **JK02** (проверено: JK-B2A8S20P, hw 19).

**ПК (Windows)**
- [Go](https://go.dev/dl/) 1.26+; Android platform-tools (`adb` в PATH); Git for Windows (Git Bash).
- Для моста BMS: JDK 17 и Android SDK (build-tools 35+, любая платформа `android-3x`) — например, из Android Studio.
- Аккаунт Telegram.

## 1. Подготовка приставки

1. Включить ADB по сети (на root-приставках обычно уже `service.adb.tcp.port=5555`). Проверить:
   ```powershell
   adb connect <BOX>
   adb -s <BOX> shell id          # uid=0(root) — adbd работает от root
   ```
2. Узнать архитектуру и найти USB-чип инвертора (кабель инвертора подключён):
   ```powershell
   adb -s <BOX> shell "uname -m; for d in /sys/bus/usb/devices/*; do [ -f \$d/idVendor ] && echo \$d \$(cat \$d/idVendor):\$(cat \$d/idProduct); done"
   ```
   Должно быть `1a86:7523` (CH340). Агент собирается под **32-битный ARM** (`GOARM=7`) даже на 64-битном ядре —
   см. [docs/DECISIONS.md](docs/DECISIONS.md).
3. Часовой пояс (пример — Киев; пояс должен быть в базе Android):
   ```powershell
   adb -s <BOX> shell "service call alarm 3 s16 Europe/Kiev; getprop persist.sys.timezone; date"
   ```
4. Убедиться, что SuperSU поддерживает `su.d` (автозапуск):
   ```powershell
   adb -s <BOX> shell "busybox strings /system/xbin/su | grep su.d"
   ```
   Должна найтись строка с `/system/su.d/`. Если нет — нужен другой способ автозапуска (см. «Адаптация»).

Без монитора Android считает экран выключенным и включает энергосбережение Wi-Fi — связь начинает терять пакеты.
Агент сам держит его выключенным (см. DECISIONS), отдельно настраивать не нужно.

## 2. Сборка

```powershell
powershell -ExecutionPolicy Bypass -File scripts\build.ps1
```
Тесты, затем `agent\build\mxg-arm` (агент), `mustprobe-arm` (зонд), `mxgview.exe` (просмотрщик).

## 3. Проверка связи с инвертором

```powershell
adb -s <BOX> push agent\build\mustprobe-arm /data/local/tmp/mustprobe
adb -s <BOX> shell "chmod 755 /data/local/tmp/mustprobe; /data/local/tmp/mustprobe -mode scan"
```
Ожидается `answered: Modbus @19200 slave 4 ...`. Другая модель Must может отвечать иначе — см. «Адаптация».

## 4. Установка агента

1. Файл `box.txt` в корне проекта: одна строка `<BOX>` (пример — `box.txt.example`). Его читают `view.bat` и скрипты.
2. Первая установка (Git Bash):
   ```bash
   export MSYS_NO_PATHCONV=1; B=$(head -1 box.txt)
   adb -s $B shell "mkdir -p /data/mxg && chmod 700 /data/mxg"
   for f in run.sh start.sh stop.sh; do adb -s $B push "agent/deploy/$f" /data/mxg/$f; done
   adb -s $B push agent/build/mxg-arm /data/mxg/mxg
   adb -s $B shell "chmod 755 /data/mxg/mxg /data/mxg/*.sh; sh /data/mxg/start.sh; sleep 15; tail /data/mxg/mxg.log"
   ```
   В журнале должно быть `first reading: grid … batt … load …`.
3. Автозапуск через SuperSU (`/system` временно перемонтируется на запись):
   ```bash
   adb -s $B push agent/deploy/90mxg /data/local/tmp/90mxg
   adb -s $B shell "mount -o rw,remount /system && mkdir -p /system/su.d && cp /data/local/tmp/90mxg /system/su.d/ && chmod 755 /system/su.d /system/su.d/90mxg; mount -o ro,remount /system; ls -l /system/su.d"
   ```
   Проверка — перезагрузить приставку и убедиться, что `/data/mxg/mxg.log` пополняется.
4. Дальнейшие обновления: `bash scripts/deploy_agent.sh` — ждёт связь и ставит агента скриптом `update.sh` на самой
   приставке (обрыв ADB посреди обновления не оставит её без агента).

## 5. Telegram-бот

1. В Telegram: @BotFather → `/newbot` → получить токен.
2. На ПК: `powershell -ExecutionPolicy Bypass -File scripts\set_token.ps1` — спросит токен, положит его на приставку
   файлом (агент перенесёт в `config.json` и удалит файл), перезапустит агента и покажет код привязки.
3. Написать боту `/pair <код>`. С этого момента бот отвечает только этому чату. Код также в
   `/data/mxg/config.json` (`pair_code`); перепривязка — очистить `owner_chat_id` и перезапустить агента.

## 6. Мост JK BMS (Bluetooth)

1. Сборка: `powershell -ExecutionPolicy Bypass -File bmsbridge\build.ps1` → `bmsbridge\build\bmsbridge.apk`.
   Ключ подписи `bmsbridge.keystore` и его пароль `keystore.pass` создаются при первой сборке — **сохраните их**:
   обновить установленный мост можно только тем же ключом.
2. Установка и запуск (закройте приложение JK на телефоне — BMS держит одно подключение):
   ```powershell
   adb -s <BOX> install -r bmsbridge\build\bmsbridge.apk
   adb -s <BOX> shell am startservice -n com.mxg.bmsbridge/.BridgeService
   ```
3. Через ~25 с посмотреть найденные устройства:
   ```powershell
   adb -s <BOX> shell "cat /data/data/com.mxg.bmsbridge/files/scan.txt /data/data/com.mxg.bmsbridge/files/bridge.log"
   ```
   Мост сам выбирает устройство с именем, начинающимся на `JK`. Многие JK BMS вещают имя = серийный номер — тогда
   задать адрес явно:
   ```powershell
   adb -s <BOX> shell am startservice -n com.mxg.bmsbridge/.BridgeService --es mac AA:BB:CC:DD:EE:FF
   ```
4. В `bridge.log` должно появиться `first frame`, в журнале агента — `bms first reading: SOC …`.
   Мост стартует сам при загрузке; агент дополнительно перезапускает его раз в 5 минут.

## 7. Просмотрщик

`view.bat` (берёт адрес из `box.txt`) → http://localhost:8787. Данные копируются в папку `data\`.

## Справка

### config.json (`/data/mxg/config.json`)

| Поле | Значение |
|---|---|
| `telegram_token` | токен бота (ставится `set_token.ps1`) |
| `owner_chat_id` | чат владельца (заполняется при `/pair`); 0 — не привязан |
| `pair_code` | одноразовый код привязки |
| `poll_seconds` | период опроса, по умолчанию 10 |
| `battery_cells` | число последовательных ячеек LiFePO4 (для запасной оценки заряда по напряжению), по умолчанию 8 |

### Файлы на приставке

`/data/mxg/`: `mxg` (агент), `run.sh` (перезапуск при падении), `start.sh`/`stop.sh`, `config.json`, `mxg.log`,
`crash.log`, `data-ДЕНЬ.csv` (инвертор), `bms-ДЕНЬ.csv`, `raw-ДЕНЬ.csv` (сырые регистры раз в 5 мин), `events.csv`,
`settings.json`, `cmd/` (очередь команд от просмотрщика). Мост: `/data/data/com.mxg.bmsbridge/files/`. Автозапуск:
`/system/su.d/90mxg`. Объём данных ≈ 3 МБ/сутки.

## Адаптация к другому железу

- **Другая приставка**: нужны root, `busybox`, ADB по сети, usbfs (`/dev/bus/usb`). Путь к управлению питанием
  USB-порта (`/sys/devices/dwc2_*/buspower`) специфичен для Amlogic — на других SoC сброс зависшего USB надо
  реализовать иначе (`agent/internal/usbfs/power.go`). Нет `su.d` — запускать `start.sh` любым доступным способом
  автозапуска (init-скрипт, приложение с BOOT_COMPLETED).
- **Другой инвертор Must**: `mustprobe -mode scan` и `-mode sweep` покажут протокол и занятые регистры; карта и
  белый список настроек — `agent/internal/must/` (`decode.go`, `faults.go`, `settings.go`).
- **Другая JK BMS**: разбор кадров — `agent/internal/jkbms/jkbms.go` (смещения JK02_32S; для JK02_24S/JK04 — см.
  esphome-jk-bms). Тест на реальных кадрах — `jkbms_test.go`.
