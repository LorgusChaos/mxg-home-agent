#!/system/bin/sh
# adbd watchdog. Never reboots, only restarts the adbd service.
#
# The bug: this adbd serves ONE TCP connection at a time and has no keepalive. If the active connection dies
# without a FIN (Wi-Fi drop, idle power save), adbd keeps it forever and every new connection is accepted but
# never read: it sits on port 5555 with unread bytes (the client's CNXN). The client sees "offline".
#
# Rule: a socket on local port 5555 with unread data (rx_queue > 0) in state ESTABLISHED or CLOSE_WAIT, seen on
# 2 checks in a row (15 s apart) = a client is locked out -> restart adbd (kill -9, init respawns it).
# No hourly cap (an early version had "6 per hour" and locked us out for an hour). At most one restart per 30 s
# follows from the 2-check rule. Also keeps Wi-Fi power save off (Android re-enables it) and logs peers.
#
# Run detached:
#   busybox setsid busybox nohup sh /data/local/tmp/adbd_watchdog.sh </dev/null >/dev/null 2>&1 &
L=/data/local/tmp/adbd_watchdog.log
B=busybox
log() { echo "$(date '+%F %T') $*" >> $L; }

# count sockets on local port 5555 (hex 15B3) with unread data, state 01 (ESTABLISHED) or 08 (CLOSE_WAIT)
stuck() {
  cat /proc/net/tcp /proc/net/tcp6 2>/dev/null | $B awk '
    $2 ~ /:15B3$/ && ($4 == "01" || $4 == "08") {
      split($5, q, ":"); if (q[2] != "00000000") n++
    }
    END { print n + 0 }'
}

# peers on port 5555 as ip:port/state/rx_queue (for the log)
peers() {
  cat /proc/net/tcp 2>/dev/null | $B awk '
    function ip(h) { return strtonum("0x" substr(h,7,2)) "." strtonum("0x" substr(h,5,2)) "." strtonum("0x" substr(h,3,2)) "." strtonum("0x" substr(h,1,2)) }
    $2 ~ /:15B3$/ && $4 != "0A" { split($3, r, ":"); split($5, q, ":"); printf "%s:%d/%s/%s ", ip(r[1]), strtonum("0x" r[2]), $4, q[2] }'
}

log "watchdog start, adbd pid=$($B pidof adbd)"
prev=0
times=""
while true; do
  # Android turns Wi-Fi power save back on; keep it off (idle power save drops the adb session)
  pm=$(wl PM 2>/dev/null)
  if [ -n "$pm" ] && [ "$pm" != "0" ]; then wl PM 0; log "wifi PM was $pm -> 0"; fi
  n=$(stuck)
  [ "$n" -gt 0 ] && log "stuck=$n peers: $(peers)"
  if [ "$n" -gt 0 ] && [ "$prev" -gt 0 ]; then
    # no hourly cap: a locked-out adbd is worse than frequent restarts (restart is cheap and harmless)
    log "locked-out clients=$n -> restart adbd (pid=$($B pidof adbd))"
    for p in $($B pidof adbd); do kill -9 $p; done
    sleep 3
    [ -z "$($B pidof adbd)" ] && start adbd && sleep 3
    log "adbd now pid=$($B pidof adbd) state=$(getprop init.svc.adbd)"
    n=0
  fi
  prev=$n
  sleep 15
done
