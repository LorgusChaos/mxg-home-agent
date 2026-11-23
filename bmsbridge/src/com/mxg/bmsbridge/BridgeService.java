package com.mxg.bmsbridge;

import android.app.Notification;
import android.app.Service;
import android.bluetooth.BluetoothAdapter;
import android.bluetooth.BluetoothDevice;
import android.bluetooth.BluetoothGatt;
import android.bluetooth.BluetoothGattCallback;
import android.bluetooth.BluetoothGattCharacteristic;
import android.bluetooth.BluetoothGattDescriptor;
import android.bluetooth.BluetoothGattService;
import android.bluetooth.BluetoothProfile;
import android.bluetooth.le.BluetoothLeScanner;
import android.bluetooth.le.ScanCallback;
import android.bluetooth.le.ScanResult;
import android.content.Intent;
import android.content.SharedPreferences;
import android.os.Handler;
import android.os.IBinder;
import android.os.Looper;

import java.io.File;
import java.io.FileOutputStream;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.HashMap;
import java.util.Locale;
import java.util.Map;
import java.util.UUID;

/**
 * JK BMS (JK02 protocol) over BLE -> raw frames in files/. Protocol per github.com/syssi/esphome-jk-bms:
 * service FFE0, characteristic FFE1 (write + notify), request = AA 55 90 EB cmd 00.. + sum byte (20 bytes),
 * 0x97 device info, 0x96 cell info (then the BMS streams). Frames: 300 bytes, start 55 AA EB 90, byte 299 =
 * sum of bytes 0..298. Every valid frame is written to files/frame_TT.hex ("time_ms\nHEX\n"), TT = frame type.
 *
 * Control: am startservice -n com.mxg.bmsbridge/.BridgeService [--es mac AA:BB:..] [--es name JK] [--ez rescan true]
 */
public class BridgeService extends Service {
    static final UUID SVC = UUID.fromString("0000ffe0-0000-1000-8000-00805f9b34fb");
    static final UUID CHR = UUID.fromString("0000ffe1-0000-1000-8000-00805f9b34fb");
    static final UUID CCCD = UUID.fromString("00002902-0000-1000-8000-00805f9b34fb");
    static final int FRAME = 300;

    private Handler h;
    private BluetoothAdapter ad;
    private BluetoothGatt gatt;
    private BluetoothGattCharacteristic wchr, nchr;
    private SharedPreferences prefs;
    private byte[] buf = new byte[0];
    private long lastFrame, connectStarted, frames;
    private boolean scanning;
    private final Map<String, String> seen = new HashMap<String, String>();
    private final Map<String, Integer> seenRssi = new HashMap<String, Integer>();

    @Override
    public IBinder onBind(Intent i) { return null; }

    @Override
    public void onCreate() {
        super.onCreate();
        h = new Handler(Looper.getMainLooper());
        prefs = getSharedPreferences("bridge", MODE_PRIVATE);
        ad = BluetoothAdapter.getDefaultAdapter();
        Notification n = new Notification.Builder(this)
                .setContentTitle("MXG BMS bridge")
                .setSmallIcon(android.R.drawable.stat_sys_data_bluetooth)
                .build();
        startForeground(1, n);
        log("service start");
        h.post(tick);
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        if (intent != null) {
            String mac = intent.getStringExtra("mac");
            String name = intent.getStringExtra("name");
            if (name != null) {
                prefs.edit().putString("name", name).apply();
                log("name filter = " + name);
            }
            if (mac != null && mac.length() == 17) {
                prefs.edit().putString("mac", mac.toUpperCase(Locale.US)).apply();
                log("mac set to " + mac);
                closeGatt();
            }
            if (intent.getBooleanExtra("rescan", false)) {
                prefs.edit().remove("mac").apply();
                log("rescan requested");
                closeGatt();
            }
        }
        return START_STICKY;
    }

    // ---------- main loop (every 10 s, on the main thread) ----------
    private final Runnable tick = new Runnable() {
        @Override
        public void run() {
            try {
                step();
            } catch (Throwable t) {
                log("tick error: " + t);
            }
            writeState();
            h.postDelayed(this, 10000);
        }
    };

    private void step() {
        long now = System.currentTimeMillis();
        if (ad == null) {
            log("no bluetooth adapter");
            return;
        }
        if (!ad.isEnabled()) {
            log("bluetooth off -> enable");
            ad.enable();
            return;
        }
        String mac = prefs.getString("mac", null);
        if (mac == null) {
            if (!scanning) startScan();
            return;
        }
        if (gatt == null) {
            connect(mac);
            return;
        }
        boolean stale = lastFrame > 0 ? now - lastFrame > 30000 : now - connectStarted > 45000;
        if (stale) {
            log("no data -> reconnect");
            closeGatt();
            return;
        }
        if (wchr != null && now - lastFrame > 8000) {
            send(0x96); // ask for cell info again if the stream stopped
        }
    }

    // ---------- scan ----------
    private final ScanCallback scb = new ScanCallback() {
        @Override
        public void onScanResult(int type, ScanResult r) {
            BluetoothDevice d = r.getDevice();
            String name = r.getScanRecord() != null ? r.getScanRecord().getDeviceName() : null;
            if (name == null) name = d.getName();
            if (name == null) name = "";
            seen.put(d.getAddress(), name);
            seenRssi.put(d.getAddress(), r.getRssi());
        }

        @Override
        public void onScanFailed(int code) {
            log("scan failed " + code);
            scanning = false;
        }
    };

    private void startScan() {
        BluetoothLeScanner sc = ad.getBluetoothLeScanner();
        if (sc == null) {
            log("no LE scanner");
            return;
        }
        seen.clear();
        seenRssi.clear();
        scanning = true;
        log("scan start");
        sc.startScan(scb);
        h.postDelayed(new Runnable() {
            @Override
            public void run() { stopScan(); }
        }, 20000);
    }

    private void stopScan() {
        BluetoothLeScanner sc = ad.getBluetoothLeScanner();
        if (sc != null) {
            try { sc.stopScan(scb); } catch (Throwable t) { log("stopScan: " + t); }
        }
        scanning = false;
        String filter = prefs.getString("name", "JK");
        StringBuilder sb = new StringBuilder();
        String best = null;
        int bestRssi = -999;
        for (Map.Entry<String, String> e : seen.entrySet()) {
            int rssi = seenRssi.get(e.getKey());
            sb.append(e.getKey()).append(' ').append(rssi).append(" dBm ").append(e.getValue()).append('\n');
            if (e.getValue().startsWith(filter) && rssi > bestRssi) {
                best = e.getKey();
                bestRssi = rssi;
            }
        }
        writeFile("scan.txt", stamp() + "\n" + sb);
        log("scan done: " + seen.size() + " devices, match=" + best);
        if (best != null) {
            prefs.edit().putString("mac", best).apply();
            log("selected " + best + " (" + seen.get(best) + ", " + bestRssi + " dBm)");
        }
    }

    // ---------- GATT ----------
    private void connect(String mac) {
        BluetoothDevice d = ad.getRemoteDevice(mac);
        connectStarted = System.currentTimeMillis();
        lastFrame = 0;
        buf = new byte[0];
        wchr = nchr = null;
        log("connect " + mac);
        gatt = d.connectGatt(this, false, gcb);
    }

    private void closeGatt() {
        if (gatt != null) {
            try { gatt.disconnect(); } catch (Throwable t) { }
            try { gatt.close(); } catch (Throwable t) { }
        }
        gatt = null;
        wchr = nchr = null;
    }

    private final BluetoothGattCallback gcb = new BluetoothGattCallback() {
        @Override
        public void onConnectionStateChange(final BluetoothGatt g, final int status, final int state) {
            h.post(new Runnable() {
                @Override
                public void run() {
                    if (g != gatt) return;
                    if (state == BluetoothProfile.STATE_CONNECTED) {
                        log("connected (status " + status + "), discovering");
                        g.discoverServices();
                    } else if (state == BluetoothProfile.STATE_DISCONNECTED) {
                        log("disconnected (status " + status + ")");
                        closeGatt();
                    }
                }
            });
        }

        @Override
        public void onServicesDiscovered(final BluetoothGatt g, final int status) {
            h.post(new Runnable() {
                @Override
                public void run() {
                    if (g != gatt) return;
                    BluetoothGattService s = g.getService(SVC);
                    if (s == null) {
                        log("service FFE0 not found (status " + status + ")");
                        closeGatt();
                        return;
                    }
                    // newer modules expose FFE1 twice: one write, one notify
                    for (BluetoothGattCharacteristic c : s.getCharacteristics()) {
                        if (!CHR.equals(c.getUuid())) continue;
                        int p = c.getProperties();
                        if ((p & (BluetoothGattCharacteristic.PROPERTY_WRITE | BluetoothGattCharacteristic.PROPERTY_WRITE_NO_RESPONSE)) != 0 && wchr == null) wchr = c;
                        if ((p & BluetoothGattCharacteristic.PROPERTY_NOTIFY) != 0 && nchr == null) nchr = c;
                    }
                    if (wchr == null || nchr == null) {
                        log("FFE1 write/notify not found");
                        closeGatt();
                        return;
                    }
                    g.setCharacteristicNotification(nchr, true);
                    BluetoothGattDescriptor d = nchr.getDescriptor(CCCD);
                    if (d != null) {
                        d.setValue(BluetoothGattDescriptor.ENABLE_NOTIFICATION_VALUE);
                        g.writeDescriptor(d);
                        log("notifications requested");
                    } else {
                        startRequests();
                    }
                }
            });
        }

        @Override
        public void onDescriptorWrite(BluetoothGatt g, BluetoothGattDescriptor d, int status) {
            h.post(new Runnable() {
                @Override
                public void run() { startRequests(); }
            });
        }

        @Override
        public void onCharacteristicChanged(BluetoothGatt g, BluetoothGattCharacteristic c) {
            final byte[] v = c.getValue();
            if (v == null) return;
            final byte[] copy = v.clone();
            h.post(new Runnable() {
                @Override
                public void run() { assemble(copy); }
            });
        }
    };

    private void startRequests() {
        h.postDelayed(new Runnable() {
            @Override
            public void run() { send(0x97); }
        }, 300);
        h.postDelayed(new Runnable() {
            @Override
            public void run() { send(0x96); }
        }, 1500);
    }

    private void send(int cmd) {
        if (gatt == null || wchr == null) return;
        byte[] f = new byte[20];
        f[0] = (byte) 0xAA; f[1] = (byte) 0x55; f[2] = (byte) 0x90; f[3] = (byte) 0xEB;
        f[4] = (byte) cmd;
        int sum = 0;
        for (int i = 0; i < 19; i++) sum += f[i] & 0xff;
        f[19] = (byte) sum;
        wchr.setWriteType(BluetoothGattCharacteristic.WRITE_TYPE_NO_RESPONSE);
        wchr.setValue(f);
        boolean ok = gatt.writeCharacteristic(wchr);
        if (!ok) log("write 0x" + Integer.toHexString(cmd) + " refused");
    }

    // ---------- frames ----------
    private void assemble(byte[] d) {
        if (buf.length > 400) buf = new byte[0];
        if (d.length >= 4 && (d[0] & 0xff) == 0x55 && (d[1] & 0xff) == 0xAA && (d[2] & 0xff) == 0xEB && (d[3] & 0xff) == 0x90) {
            buf = new byte[0];
        }
        byte[] n = new byte[buf.length + d.length];
        System.arraycopy(buf, 0, n, 0, buf.length);
        System.arraycopy(d, 0, n, buf.length, d.length);
        buf = n;
        if (buf.length < FRAME) return;
        int sum = 0;
        for (int i = 0; i < FRAME - 1; i++) sum += buf[i] & 0xff;
        if ((sum & 0xff) != (buf[FRAME - 1] & 0xff)) {
            log("frame CRC mismatch");
            buf = new byte[0];
            return;
        }
        int type = buf[4] & 0xff;
        StringBuilder hex = new StringBuilder(FRAME * 2);
        for (int i = 0; i < FRAME; i++) hex.append(String.format(Locale.US, "%02x", buf[i] & 0xff));
        writeFile(String.format(Locale.US, "frame_%02x.hex", type), System.currentTimeMillis() + "\n" + hex + "\n");
        if (frames == 0) log("first frame, type 0x" + Integer.toHexString(type));
        frames++;
        lastFrame = System.currentTimeMillis();
        buf = new byte[0];
    }

    // ---------- files ----------
    private void writeFile(String name, String content) {
        try {
            File tmp = new File(getFilesDir(), name + ".tmp");
            FileOutputStream o = new FileOutputStream(tmp);
            o.write(content.getBytes("UTF-8"));
            o.close();
            tmp.renameTo(new File(getFilesDir(), name));
        } catch (Throwable t) {
            // nothing sensible to do
        }
    }

    private void writeState() {
        writeFile("state.txt", stamp() + "\nmac=" + prefs.getString("mac", "") + "\nconnected=" + (gatt != null && wchr != null)
                + "\nframes=" + frames + "\nlast_frame_ms=" + lastFrame + "\nscanning=" + scanning + "\n");
    }

    private static String stamp() {
        return new SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.US).format(new Date());
    }

    private void log(String s) {
        try {
            File f = new File(getFilesDir(), "bridge.log");
            if (f.length() > 256 * 1024) f.renameTo(new File(getFilesDir(), "bridge.log.1"));
            FileOutputStream o = new FileOutputStream(f, true);
            o.write((stamp() + " " + s + "\n").getBytes("UTF-8"));
            o.close();
        } catch (Throwable t) {
            // ignore
        }
    }
}
