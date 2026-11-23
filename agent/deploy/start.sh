#!/system/bin/sh
# Starts the mxg supervisor (run.sh) detached, unless it is already running.
# Called at boot by SuperSU from /system/su.d/90mxg, or by hand / over adb.
# Waits until run.sh is up: if the caller (adb shell) exits immediately, a child that has not yet left the
# caller's process group can be killed with it.
W="sh /data/mxg/run.sh "
running() {
  for p in /proc/[0-9]*; do
    [ "$(cat $p/cmdline 2>/dev/null | tr '\0' ' ')" = "$W" ] && return 0
  done
  return 1
}
running && { echo "mxg already running"; exit 0; }
busybox setsid busybox nohup sh /data/mxg/run.sh </dev/null >/dev/null 2>&1 &
i=0
while [ $i -lt 10 ]; do
  sleep 1
  running && { sleep 2; echo "mxg started"; exit 0; }
  i=$((i + 1))
done
echo "mxg did not start"
exit 1
