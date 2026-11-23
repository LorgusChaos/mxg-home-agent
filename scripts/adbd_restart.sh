#!/system/bin/sh
# Restart a hung adbd WITHOUT rebooting the box.
# Tries several methods in turn, stops at the first one that brings port 5555 back.
# Run detached (killing adbd kills the adb shell that started it):
#   busybox setsid busybox nohup sh /data/local/tmp/adbd_restart.sh </dev/null >/dev/null 2>&1 &
L=/data/local/tmp/adbd_restart.log
log() { echo "$(date '+%F %T') $*" >> $L; }
up()  { busybox netstat -tln | grep -q ':5555 '; }

log "begin: pid=$(busybox pidof adbd) state=$(getprop init.svc.adbd)"

# 1. kill the hung process; init may respawn it by itself
for p in $(busybox pidof adbd); do kill -9 $p; done
sleep 3
log "after kill: pid=$(busybox pidof adbd) state=$(getprop init.svc.adbd)"
up && { log "ok (respawned by init)"; exit 0; }

# 2. ask init to start it
start adbd; sleep 4
up && { log "ok (start)"; exit 0; }

# 3. full stop/start cycle in init
stop adbd; sleep 1; start adbd; sleep 4
up && { log "ok (stop/start)"; exit 0; }

# 4. the path init itself uses at boot: toggle USB config
setprop sys.usb.config none; sleep 2
setprop sys.usb.config mtp,adb; sleep 5
up && { log "ok (usb config toggle)"; exit 0; }

# 5. last resort: launch adbd directly, outside init
busybox setsid /sbin/adbd --root_seclabel=u:r:su:s0 </dev/null >/dev/null 2>&1 &
sleep 4
up && { log "ok (direct launch)"; exit 0; }

log "FAIL: adbd not listening, no reboot done"
exit 1
