package com.vpnplatform.vpn_app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import android.util.Log
import com.vpnplatform.vpncore.Protector
import com.vpnplatform.vpncore.Vpncore
import java.io.File
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit

/**
 * Creates the TUN interface and hands its file descriptor to Xray-core (vpncore.aar).
 * Xray's own sockets are excluded from the tunnel via VpnService.protect().
 *
 * Reconnect on network changes: Wi-Fi ↔ mobile switches, losing and regaining
 * signal, etc. kill every connection Xray has to the node, and some stay
 * half-open for minutes. The service watches the default (non-VPN) network;
 * when it changes or comes back after a loss, the tunnel is rebuilt on the new
 * network (debounced, ~1 s of downtime). While there is no network at all the
 * tunnel stays up and shows "等待网络恢复" so apps don't fall back to the
 * unprotected network.
 */
class XrayVpnService : VpnService() {
    companion object {
        const val ACTION_CONNECT = "com.vpnplatform.CONNECT"
        const val ACTION_DISCONNECT = "com.vpnplatform.DISCONNECT"
        const val EXTRA_NAME = "name"
        private const val TAG = "XrayVpnService"
        private const val CHANNEL_ID = "vpn"
        private const val NOTIFICATION_ID = 1

        /** The profile is stored on disk so always-on VPN / service restarts can reconnect. */
        fun profileFile(ctx: Context) = File(ctx.noBackupFilesDir, "profile.json")
    }

    private val worker = Executors.newSingleThreadScheduledExecutor()
    private var tun: ParcelFileDescriptor? = null
    private var sessionName = "VPN"

    // ---- network watching (all fields touched on the worker thread) ----
    private var netCallback: ConnectivityManager.NetworkCallback? = null
    private var currentNet: Network? = null
    private val available = mutableListOf<Network>()
    private var lostNetwork = false
    private var pendingRestart: ScheduledFuture<*>? = null
    private var restarts = 0

    // The newest start command. A stop only ends the service if no newer
    // start arrived meanwhile (stopSelf(id)): otherwise a quick disconnect →
    // connect could end with the core running in a destroyed service and the
    // app showing "connected" with no tunnel.
    @Volatile private var lastStartId = 0

    override fun onCreate() {
        super.onCreate()
        // This service has a process of its own (":vpn", manifest): its
        // status reaches the UI process through VpnState.
        CrashCatcher.install(this)
        VpnState.init(this)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        lastStartId = startId
        if (intent?.action == ACTION_DISCONNECT) {
            worker.execute { stopVpn(startId = startId) }
            return START_NOT_STICKY
        }
        // ACTION_CONNECT, or started by the system for always-on VPN (action = android.net.VpnService).
        val name = intent?.getStringExtra(EXTRA_NAME) ?: "VPN"
        sessionName = name
        startForegroundCompat(name)
        worker.execute {
            restarts = 0
            pendingRetry?.cancel(false)
            pendingRetry = null
            // On the worker, in command order: a disconnect queued before
            // this connect has already removed the marker.
            Interruption.on(this)
            // An explicit connect while running (e.g. new node) replaces the tunnel.
            if (intent?.action == ACTION_CONNECT && Vpncore.isRunning()) cleanup()
            // A user connect reports a failure right away; a start by the system
            // (always-on VPN, START_STICKY restart) keeps retrying like a restart.
            startVpn(name, retry = intent?.action != ACTION_CONNECT)
        }
        return START_STICKY
    }

    // Rebuilding after a network change / a system restart can fail while the
    // network is still settling. The service then retries on its own (it is
    // already in the foreground), because the app could not restart it from
    // the background on Android 12+.
    private var pendingRetry: ScheduledFuture<*>? = null
    private var retryAttempt = 0

    private fun scheduleRetry(name: String, reason: String?) {
        if (retryAttempt >= 6) {
            retryAttempt = 0
            Interruption.off(this) // given up and reported: not an interruption
            VpnState.set("error", reason ?: "连接失败")
            stopForeground(STOP_FOREGROUND_REMOVE)
            stopSelf(lastStartId)
            return
        }
        val secs = minOf(30L, 2L shl retryAttempt) // 2, 4, 8, 16, 30, 30
        retryAttempt++
        VpnState.set("connecting", "连接中断，正在自动重试…")
        pendingRetry = worker.schedule({
            pendingRetry = null
            startVpn(name, retry = true)
        }, secs, TimeUnit.SECONDS)
    }

    private fun startVpn(name: String, retry: Boolean = false) {
        if (Vpncore.isRunning()) {
            VpnState.set("connected")
            return
        }
        VpnState.set("connecting")
        try {
            val config = profileFile(this).takeIf { it.exists() }?.readText()
                ?: throw IllegalStateException("请先在应用内登录并连接")
            // Compiled profiles carry their geo data; the core needs no asset files.
            val assetDir = filesDir.absolutePath

            val builder = Builder()
                .setSession(name)
                .setMtu(1500)
                .addAddress("172.19.0.1", 30)
                .addRoute("0.0.0.0", 0)
                .addDnsServer("172.19.0.2")
                // IPv6: route it into the tunnel too, so nothing leaks around the VPN.
                .addAddress("fdfe:dcba:9876::1", 126)
                .addRoute("::", 0)
                // The app's own traffic (API calls, Xray) goes around the tunnel.
                .addDisallowedApplication(packageName)
                .setConfigureIntent(launchIntent())
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) builder.setMetered(false)

            val pfd = builder.establish() ?: throw IllegalStateException("VPN 权限被拒绝")
            tun = pfd
            // Keep the Go heap of the tunnel bounded (see vpncore.SetSoftMemoryLimit).
            Vpncore.setSoftMemoryLimit(48)
            Vpncore.start(config, pfd.fd, assetDir, object : Protector {
                override fun protect(fd: Int): Boolean = this@XrayVpnService.protect(fd)
            })
            currentNet?.let { setUnderlyingNetworks(arrayOf(it)) }
            watchNetwork()
            retryAttempt = 0
            VpnState.set("connected")
        } catch (e: Exception) {
            Log.e(TAG, "start failed", e)
            cleanup()
            // No profile / no permission can't get better by retrying.
            val permanent = e is IllegalStateException
            if (retry && !permanent) {
                scheduleRetry(name, e.message)
                return
            }
            unwatchNetwork()
            Interruption.off(this)
            VpnState.set("error", e.message)
            stopSelf(lastStartId)
        }
    }

    /** reason != null tells the app the stop was not the user's choice (don't auto-reconnect). */
    private fun stopVpn(reason: String? = null, startId: Int = lastStartId) {
        pendingRetry?.cancel(false)
        pendingRetry = null
        retryAttempt = 0
        Interruption.off(this) // the user's stop, or another VPN took over
        VpnState.set("disconnecting")
        unwatchNetwork()
        cleanup()
        VpnState.set("disconnected", reason)
        // A connect queued behind this stop keeps the service (and its
        // foreground notification); it runs right after on this thread.
        if (startId != lastStartId) return
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf(startId)
    }

    private fun cleanup() {
        try {
            Vpncore.stop()
        } catch (e: Exception) {
            Log.w(TAG, "stop core", e)
        }
        try {
            tun?.close()
        } catch (_: Exception) {
        }
        tun = null
    }

    // ---- reconnect on network change ----

    private fun watchNetwork() {
        if (netCallback != null) return
        val cm = getSystemService(ConnectivityManager::class.java) ?: return
        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                Log.w(TAG, "default network: $network")
                worker.execute { onNetworkAvailable(network) }
            }

            override fun onLost(network: Network) {
                Log.w(TAG, "default network lost: $network")
                worker.execute { onNetworkLost(network) }
            }
        }
        // Start from the network the system actually uses. This app is excluded
        // from its own VPN, so its active network is the underlying one; the
        // first callback is not necessarily it (e.g. mobile data reported
        // before Wi-Fi), and following that one would rebuild the tunnel on
        // the wrong network changes.
        currentNet = cm.activeNetwork
        Log.w(TAG, "start network: $currentNet")
        try {
            // Listen (not request) for real, non-VPN internet networks; the
            // default-network callback would report our own VPN network.
            val req = NetworkRequest.Builder()
                .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
                .build()
            cm.registerNetworkCallback(req, cb)
            netCallback = cb
        } catch (e: Exception) {
            Log.w(TAG, "network callback", e)
        }
    }

    private fun unwatchNetwork() {
        pendingRestart?.cancel(false)
        pendingRestart = null
        val cb = netCallback ?: return
        netCallback = null
        try {
            getSystemService(ConnectivityManager::class.java)?.unregisterNetworkCallback(cb)
        } catch (_: Exception) {
        }
        currentNet = null
        available.clear()
        lostNetwork = false
    }

    private fun onNetworkAvailable(network: Network) {
        if (network !in available) available.add(network)
        // Keep using the current network while it's alive; a second network
        // appearing (e.g. mobile data alongside Wi-Fi) is not a switch.
        if (currentNet != null && currentNet != network && !lostNetwork) return
        val previous = currentNet
        currentNet = network
        if (!Vpncore.isRunning()) return
        setUnderlyingNetworks(arrayOf(network))
        // First callback after start just reports the current network.
        if (previous == null && !lostNetwork) return
        if (previous == network && !lostNetwork) return
        Log.w(TAG, "network changed (lost=$lostNetwork), rebuilding tunnel")
        lostNetwork = false
        scheduleRestart()
    }

    private fun onNetworkLost(network: Network) {
        available.remove(network)
        if (network != currentNet) return
        val next = available.lastOrNull()
        if (next != null) {
            // e.g. Wi-Fi dropped while mobile data is up: move to it now.
            currentNet = null
            onNetworkAvailable(next)
            return
        }
        currentNet = null
        lostNetwork = true
        if (Vpncore.isRunning()) VpnState.set("connecting", "网络已断开，恢复后自动重连")
    }

    /** Debounced: networks often flap several times while switching. */
    private fun scheduleRestart() {
        pendingRestart?.cancel(false)
        pendingRestart = worker.schedule({
            pendingRestart = null
            if (netCallback == null) return@schedule // user disconnected meanwhile
            restarts++
            VpnState.set("connecting", "网络已切换，正在重连…")
            cleanup()
            startVpn(sessionName, retry = true)
        }, 1200, TimeUnit.MILLISECONDS)
    }

    override fun onRevoke() {
        // Another VPN app took over or the user revoked permission in Settings.
        worker.execute { stopVpn("revoked", lastStartId) }
    }

    override fun onDestroy() {
        worker.execute {
            unwatchNetwork()
            cleanup()
        }
        worker.shutdown()
        if (VpnState.status != "error") VpnState.set("disconnected")
        super.onDestroy()
    }

    private fun launchIntent(): PendingIntent = PendingIntent.getActivity(
        this, 0, Intent(this, MainActivity::class.java),
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )

    private fun startForegroundCompat(name: String) {
        val nm = getSystemService(NotificationManager::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            nm.createNotificationChannel(
                NotificationChannel(CHANNEL_ID, "VPN 状态", NotificationManager.IMPORTANCE_LOW)
            )
        }
        val stop = PendingIntent.getService(
            this, 1, Intent(this, XrayVpnService::class.java).setAction(ACTION_DISCONNECT),
            PendingIntent.FLAG_IMMUTABLE,
        )
        // Notification.Builder(context, channel) is API 26+; minSdk is 24.
        val b = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Notification.Builder(this, CHANNEL_ID)
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(this).setPriority(Notification.PRIORITY_LOW)
        }
        val n: Notification = b
            .setSmallIcon(android.R.drawable.ic_lock_lock)
            .setContentTitle("环游 VPN 已开启")
            .setContentText(name)
            .setContentIntent(launchIntent())
            .setOngoing(true)
            .addAction(Notification.Action.Builder(null, "断开", stop).build())
            .build()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            // systemExempted is the type Android documents for VPN apps; if the
            // system does not grant it on this device, specialUse still works.
            try {
                startForeground(NOTIFICATION_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SYSTEM_EXEMPTED)
            } catch (e: Exception) {
                Log.w(TAG, "systemExempted refused, using specialUse", e)
                startForeground(NOTIFICATION_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
            }
        } else {
            startForeground(NOTIFICATION_ID, n)
        }
    }
}
