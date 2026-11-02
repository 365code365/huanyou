package com.vpnplatform.vpn_app

import android.app.ActivityManager
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.Process
import android.util.Log
import org.json.JSONObject
import java.io.File

/**
 * VPN status, shared by two processes.
 *
 * XrayVpnService runs in its own process (":vpn", see the manifest). When the
 * phone is short of memory the system can reclaim the UI process (the
 * Flutter engine, well over 100 MB) without taking the tunnel down with it,
 * and the small tunnel process is far less likely to be picked than one big
 * process holding both.
 *
 * The service sets the status ([set]). It reaches the UI process as a file
 * (the current state, read when the UI starts) and a broadcast (each
 * change); [Watcher] is the UI side.
 */
object VpnState {
    const val PROCESS_SUFFIX = ":vpn"
    private const val ACTION = "com.vpnplatform.vpn_app.VPN_STATE"
    private const val FILE = "vpn_state.json"
    private const val TAG = "VpnState"

    /** pid / seq order the updates of one VPN process (a broadcast can arrive after a newer file). */
    data class Snapshot(val status: String, val message: String?, val since: Long, val pid: Int = 0, val seq: Long = 0) {
        val active get() = status == "connecting" || status == "connected" || status == "disconnecting"
        fun sameState(o: Snapshot) = status == o.status && message == o.message && since == o.since
    }

    // Signature-level (manifest): only this app can send or receive the status.
    private fun permission(c: Context) = c.packageName + ".permission.VPN_STATE"
    private fun file(c: Context) = File(c.noBackupFilesDir, FILE)

    // ---- VPN process: the source ----

    @Volatile private var ctx: Context? = null
    private var seq = 0L

    @Volatile var status: String = "disconnected"
        private set

    /** When the current session first connected (ms), kept across rebuilds on
     *  network changes; 0 when down. Lets a re-created UI show the real
     *  connected time instead of starting from zero. */
    @Volatile var since: Long = 0L
        private set

    fun init(c: Context) {
        ctx = c.applicationContext
    }

    @Synchronized
    fun set(status: String, message: String? = null) {
        if (status == "connected" && since == 0L) since = System.currentTimeMillis()
        if (status == "disconnected" || status == "error") since = 0L
        this.status = status
        val c = ctx ?: return
        seq++
        val pid = Process.myPid()
        try {
            val j = JSONObject().put("status", status).put("since", since).put("pid", pid).put("seq", seq)
            if (message != null) j.put("message", message)
            val f = file(c)
            val tmp = File(f.parentFile, f.name + ".tmp")
            tmp.writeText(j.toString())
            if (!tmp.renameTo(f)) f.writeText(j.toString())
        } catch (e: Exception) {
            Log.w(TAG, "save state", e)
        }
        c.sendBroadcast(
            Intent(ACTION).setPackage(c.packageName)
                .putExtra("status", status).putExtra("message", message).putExtra("since", since)
                .putExtra("pid", pid).putExtra("seq", seq),
            permission(c),
        )
    }

    // ---- UI process ----

    /**
     * The VPN process's last state. A tunnel whose process is gone (killed by
     * the system, crashed) reads as disconnected; so does an old error (it
     * was shown when it happened).
     */
    fun read(c: Context): Snapshot {
        val j = try {
            JSONObject(file(c).readText())
        } catch (_: Exception) {
            return Snapshot("disconnected", null, 0)
        }
        val pid = j.optInt("pid")
        val seq = j.optLong("seq")
        val s = Snapshot(
            j.optString("status", "disconnected"),
            if (j.has("message")) j.optString("message") else null,
            j.optLong("since"), pid, seq,
        )
        if (s.status == "error" || (s.active && !alive(c, pid))) return Snapshot("disconnected", null, 0, pid, seq)
        return s
    }

    /** Whether pid is this app's VPN process (pids are reused). */
    fun alive(c: Context, pid: Int): Boolean {
        if (pid <= 0) return false
        val name = c.packageName + PROCESS_SUFFIX
        val list = try {
            c.getSystemService(ActivityManager::class.java)?.runningAppProcesses
        } catch (_: Exception) {
            null
        }
        if (list != null && list.any { it.processName == name }) return list.any { it.pid == pid && it.processName == name }
        // Not listed: gone, or a system that lists only the calling process.
        // /proc tells (this app's own processes are readable, others' are not).
        return try {
            File("/proc/$pid/cmdline").readText().trimEnd('\u0000') == name
        } catch (_: Exception) {
            false
        }
    }

    /**
     * Delivers the VPN status to the UI on the main thread: every broadcast
     * from the VPN process, plus a check while the app is in the foreground
     * that the process is still there (a killed process sends nothing).
     */
    object Watcher {
        private val main = Handler(Looper.getMainLooper())
        private val listeners = mutableSetOf<(Snapshot) -> Unit>()
        private var app: Context? = null
        private var receiver: BroadcastReceiver? = null
        private var last = Snapshot("disconnected", null, 0)
        private var resumed = false

        private val poll = object : Runnable {
            override fun run() {
                if (!resumed) return
                check()
                main.postDelayed(this, 2000)
            }
        }

        /** Main thread. The listener gets the current state right away. */
        fun add(c: Context, l: (Snapshot) -> Unit) {
            val a = c.applicationContext
            app = a
            listeners.add(l)
            if (receiver == null) {
                val r = object : BroadcastReceiver() {
                    override fun onReceive(context: Context, i: Intent) {
                        val s = Snapshot(
                            i.getStringExtra("status") ?: "disconnected", i.getStringExtra("message"),
                            i.getLongExtra("since", 0L), i.getIntExtra("pid", 0), i.getLongExtra("seq", 0L),
                        )
                        if (s.pid == last.pid && s.seq <= last.seq) return // older than what we have
                        publish(s)
                    }
                }
                val f = IntentFilter(ACTION)
                if (Build.VERSION.SDK_INT >= 33) {
                    a.registerReceiver(r, f, permission(a), null, Context.RECEIVER_NOT_EXPORTED)
                } else {
                    a.registerReceiver(r, f, permission(a), null)
                }
                receiver = r
            }
            last = read(a)
            l(last)
        }

        fun remove(l: (Snapshot) -> Unit) {
            listeners.remove(l)
            if (listeners.isEmpty()) {
                receiver?.let { r -> try { app?.unregisterReceiver(r) } catch (_: Exception) {} }
                receiver = null
            }
        }

        /** The app is in the foreground: check now, then every 2 s. */
        fun resume(c: Context) {
            app = c.applicationContext
            resumed = true
            main.removeCallbacks(poll)
            poll.run()
        }

        fun pause() {
            resumed = false
            main.removeCallbacks(poll)
        }

        // Only a dead VPN process matters here: while it lives, its
        // broadcasts carry every change (and the error message of a failure).
        private fun check() {
            val a = app ?: return
            if (!last.active || listeners.isEmpty() || alive(a, last.pid)) return
            // Gone without a word (killed, crashed). read() gives a process
            // the system already restarted, or "disconnected".
            publish(read(a))
        }

        private fun publish(s: Snapshot) {
            val changed = !s.sameState(last)
            last = s
            if (changed) listeners.toList().forEach { it(s) }
        }
    }
}
