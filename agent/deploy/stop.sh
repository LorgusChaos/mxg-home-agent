#!/system/bin/sh
# Stops the supervisor and the agent (for updates).
for W in "sh /data/mxg/run.sh " "/data/mxg/mxg -dir /data/mxg "; do
  for p in /proc/[0-9]*; do
    [ "$(cat $p/cmdline 2>/dev/null | tr '\0' ' ')" = "$W" ] && kill ${p#/proc/}
  done
done
exit 0
