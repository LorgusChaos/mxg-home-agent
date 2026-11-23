#!/system/bin/sh
# On-box updater. Runs detached (setsid), so a dropped adb session cannot leave the agent stopped halfway.
# Usage: sh /data/local/tmp/update.sh <expected size of /data/mxg/mxg.new or 0>
# Installs /system/su.d/90mxg (autostart), start.sh, and the new agent binary if its size matches.
L=/data/mxg/update.log
exec >>$L 2>&1
echo "$(date '+%F %T') update start, expected size $1"

if [ -f /data/local/tmp/90mxg ]; then
  mount -o rw,remount /system && {
    mkdir -p /system/su.d
    cp /data/local/tmp/90mxg /system/su.d/90mxg
    chmod 755 /system/su.d /system/su.d/90mxg
    sync
    mount -o ro,remount /system || echo "remount ro failed (busy), stays rw until reboot"
  }
  ls -l /system/su.d
fi

for f in start.sh stop.sh run.sh; do
  [ -f /data/local/tmp/$f ] && cp /data/local/tmp/$f /data/mxg/$f && chmod 755 /data/mxg/$f
done

if [ -f /data/mxg/mxg.new ] && [ "$1" != "0" ]; then
  sz=$(busybox wc -c < /data/mxg/mxg.new | busybox tr -d ' ')  # busybox 1.22 here has no "stat -c"
  if [ "$sz" = "$1" ]; then
    sh /data/mxg/stop.sh
    sleep 2
    mv /data/mxg/mxg.new /data/mxg/mxg
    chmod 755 /data/mxg/mxg
    echo "binary replaced"
  else
    echo "mxg.new size $sz != $1, not replaced"
    rm -f /data/mxg/mxg.new
  fi
fi

sh /data/mxg/start.sh
echo "$(date '+%F %T') update done"
