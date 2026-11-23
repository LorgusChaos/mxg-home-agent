#!/system/bin/sh
# Keeps mxg running: restarts it 10 s after any exit. Never reboots the box.
# Panics/stderr go to crash.log (trimmed when it passes 256 KB).
D=/data/mxg
while true; do
  if [ -f $D/crash.log ] && [ $(busybox wc -c < $D/crash.log) -gt 262144 ]; then
    busybox tail -c 65536 $D/crash.log > $D/crash.log.tmp && mv $D/crash.log.tmp $D/crash.log
  fi
  $D/mxg -dir $D </dev/null >/dev/null 2>>$D/crash.log
  echo "$(date '+%F %T') mxg exited with $?, restart in 10 s" >> $D/crash.log
  sleep 10
done
