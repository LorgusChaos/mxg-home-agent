#!/bin/bash
# Deploys agent/build/mxg-arm and agent/deploy/* to the box over a flaky Wi-Fi/ADB link.
# Retries until the box answers, pushes the files, then runs agent/deploy/update.sh ON THE BOX detached
# (setsid), so a dropped adb session cannot leave the agent stopped halfway.
# Usage (Git Bash):  bash scripts/deploy_agent.sh [box_ip:port]   (default: $MXG_BOX, then box.txt)
export MSYS_NO_PATHCONV=1
ROOT=$(cd "$(dirname "$0")/.." && pwd -W 2>/dev/null || pwd)   # Windows path for adb.exe
T=${1:-$MXG_BOX}
[ -z "$T" ] && [ -f "$ROOT/box.txt" ] && T=$(head -1 "$ROOT/box.txt" | tr -d '\r\n ')
[ -z "$T" ] && { echo "box address not set: pass IP:5555, set MXG_BOX or create box.txt"; exit 2; }
A="$ROOT/agent"
SIZE=$(wc -c < "$A/build/mxg-arm" | tr -d ' ')
end=$(( $(date +%s) + 3600 ))
step=small
while [ "$(date +%s)" -lt "$end" ]; do
  st=$(adb devices | grep "^$T" | awk '{print $2}')
  [ -z "$st" ] && adb connect "$T" >/dev/null 2>&1
  if [ "$st" = "device" ]; then
    if [ $step = small ]; then
      ok=1
      for f in 90mxg start.sh stop.sh run.sh update.sh; do
        adb -s "$T" push "$A/deploy/$f" "/data/local/tmp/$f" >/dev/null 2>&1 || ok=0
      done
      [ $ok = 1 ] && { echo "$(date +%T) scripts pushed"; step=bin; }
    fi
    if [ $step = bin ]; then
      if adb -s "$T" push "$A/build/mxg-arm" /data/mxg/mxg.new >/dev/null 2>&1; then
        echo "$(date +%T) binary pushed"; step=run
      else
        echo "$(date +%T) binary push failed, retry"
      fi
    fi
    if [ $step = run ]; then
      if adb -s "$T" shell "busybox setsid busybox nohup sh /data/local/tmp/update.sh $SIZE </dev/null >/dev/null 2>&1 & sleep 2; echo fired" 2>/dev/null | grep -q fired; then
        echo "$(date +%T) updater started (size $SIZE)"; step=check
      fi
    fi
    if [ $step = check ]; then
      sleep 20
      out=$(adb -s "$T" shell 'tail -8 /data/mxg/update.log; grep " start," /data/mxg/mxg.log | tail -1' 2>/dev/null)
      if echo "$out" | grep -q "binary replaced"; then
        echo "$out"
        exit 0
      fi
    fi
  fi
  sleep 3
done
echo "gave up at $(date +%T), step=$step"
exit 1
