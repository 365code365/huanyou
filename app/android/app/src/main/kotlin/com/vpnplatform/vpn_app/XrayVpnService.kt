package com.vpnplatform.vpn_app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import android.util.Log
import com.vpnplatform.vpncore.Protector
import com.vpnplatform.vpncore.Vpncore
import java.io.File
import java.util.concurrent.Executors

/**
 * Creates the TUN interface and hands its file descriptor to Xray-core (vpncore.aar).
 * Xray's own sockets are excluded from the tunnel via VpnService.protect().
 */
class XrayVpnService : VpnService() {
    companion object {
        const val ACTION_CONNECT = "com.vpnplatform.CONNECT"
        const val ACTION_DISCONNECT = "com.vpnplatform.DISCONNECT"
        const val EXTRA_NAME = "name"
        private const val TAG = "XrayVpnService"
        private const val CHANNEL_ID = "vpn"
        private const val NOTIFICATION_ID = 1
        private const val GEO_FILE = "geoip-only-cn-private.dat"

        /** The profile is stored on disk so always-on VPN / service restarts can reconnect. */
        fun profileFile(ctx: Context) = File(ctx.noBackupFilesDir, "profile.json")
    }

    private val worker = Executors.newSingleThreadExecutor()
    private var tun: ParcelFileDescriptor? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_DISCONNECT) {
            worker.execute { stopVpn() }
            return START_NOT_STICKY
        }
        // ACTION_CONNECT, or started by the system for always-on VPN (action = android.net.VpnService).
        val name = intent?.getStringExtra(EXTRA_NAME) ?: "VPN"
        startForegroundCompat(name)
        worker.execute { startVpn(name) }
        return START_STICKY
    }

    private fun startVpn(name: String) {
        if (Vpncore.isRunning()) {
            VpnState.set("connected")
            return
        }
        VpnState.set("connecting")
        try {
            val config = profileFile(this).takeIf { it.exists() }?.readText()
                ?: throw IllegalStateException("请先在应用内登录并连接")
            val assetDir = prepareAssets()

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
            Vpncore.start(config, pfd.fd, assetDir, object : Protector {
                override fun protect(fd: Int): Boolean = this@XrayVpnService.protect(fd)
            })
            VpnState.set("connected")
        } catch (e: Exception) {
            Log.e(TAG, "start failed", e)
            cleanup()
            VpnState.set("error", e.message)
            stopSelf()
        }
    }

    private fun stopVpn() {
        VpnState.set("disconnecting")
        cleanup()
        VpnState.set("disconnected")
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
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

    /** Copies the geoip file out of the APK so Xray can read it from disk. */
    private fun prepareAssets(): String {
        val dir = File(filesDir, "xray").apply { mkdirs() }
        val out = File(dir, GEO_FILE)
        val stamp = File(dir, "$GEO_FILE.version")
        val version = packageManager.getPackageInfo(packageName, 0).lastUpdateTime.toString()
        if (!out.exists() || !stamp.exists() || stamp.readText() != version) {
            assets.open("flutter_assets/assets/geo/$GEO_FILE").use { input ->
                out.outputStream().use { input.copyTo(it) }
            }
            stamp.writeText(version)
        }
        return dir.absolutePath
    }

    override fun onRevoke() {
        // Another VPN app took over or the user revoked permission in Settings.
        worker.execute { stopVpn() }
    }

    override fun onDestroy() {
        worker.execute { cleanup() }
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
        val n: Notification = Notification.Builder(this, CHANNEL_ID)
            .setSmallIcon(android.R.drawable.ic_lock_lock)
            .setContentTitle("已连接")
            .setContentText(name)
            .setContentIntent(launchIntent())
            .setOngoing(true)
            .addAction(Notification.Action.Builder(null, "断开", stop).build())
            .build()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startForeground(NOTIFICATION_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        } else {
            startForeground(NOTIFICATION_ID, n)
        }
    }
}
