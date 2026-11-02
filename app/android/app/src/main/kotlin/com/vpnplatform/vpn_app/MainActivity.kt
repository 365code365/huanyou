package com.vpnplatform.vpn_app

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.net.VpnService
import android.os.Build
import android.os.PowerManager
import android.provider.Settings
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.EventChannel
import io.flutter.plugin.common.MethodChannel

class MainActivity : FlutterActivity() {
    companion object {
        private const val REQ_VPN = 0x7601
        private const val REQ_NOTIF = 0x7602
    }

    private var pendingName: String? = null
    private var pendingResult: MethodChannel.Result? = null

    // The status listener of this engine. The VPN service outlives the
    // activity, so it is removed when the engine goes; otherwise every reopen
    // would add one more listener posting to a dead engine.
    private var statusListener: ((VpnState.Snapshot) -> Unit)? = null

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        CrashCatcher.install(this)
        val messenger = flutterEngine.dartExecutor.binaryMessenger

        MethodChannel(messenger, "vpn_platform/vpn").setMethodCallHandler { call, result ->
            when (call.method) {
                // The tunnel runs in its own process (VpnState).
                "status" -> result.success(VpnState.read(this).status)
                // Why the tunnel stopped while the app was away (null: it didn't).
                "takeInterruption" -> result.success(Interruption.take(this))
                "connect" -> {
                    val config = call.argument<String>("config")
                    if (config.isNullOrEmpty()) {
                        result.error("bad_args", "缺少配置", null)
                        return@setMethodCallHandler
                    }
                    // Atomic: the service may read the profile at the same time
                    // (a network-change restart) and must never see half a file.
                    val f = XrayVpnService.profileFile(this)
                    val tmp = java.io.File(f.parentFile, f.name + ".tmp")
                    tmp.writeText(config)
                    if (!tmp.renameTo(f)) {
                        f.writeText(config)
                        tmp.delete()
                    }
                    connect(call.argument<String>("name") ?: "VPN", result)
                }
                "disconnect" -> {
                    startService(Intent(this, XrayVpnService::class.java).setAction(XrayVpnService.ACTION_DISCONNECT))
                    result.success(null)
                }
                else -> result.notImplemented()
            }
        }

        // Stable per-device id (ANDROID_ID). It survives app reinstall, so a
        // returning user is recognized without any login.
        MethodChannel(messenger, "vpn_platform/device").setMethodCallHandler { call, result ->
            when (call.method) {
                "deviceId" -> {
                    val id = Settings.Secure.getString(contentResolver, Settings.Secure.ANDROID_ID)
                    result.success(id)
                }
                // Primary CPU ABI, so the update check offers the matching APK.
                "abi" -> result.success(Build.SUPPORTED_ABIS.firstOrNull() ?: "")
                "model" -> result.success("${Build.MANUFACTURER} ${Build.MODEL} (Android ${Build.VERSION.RELEASE})")
                // Integrity report for the API (Integrity.kt); off the main thread.
                "integrity" -> Thread {
                    val m = Integrity.check(applicationContext)
                    runOnUiThread { result.success(m) }
                }.start()
                // Native crash saved by CrashCatcher in the previous run; returned once.
                "takeCrash" -> result.success(CrashCatcher.take(this))
                // App cache (images, WebView, temp files): size in bytes / clear.
                "cacheSize" -> result.success(dirSize(cacheDir) + (externalCacheDir?.let { dirSize(it) } ?: 0L))
                "clearCache" -> {
                    val freed = dirSize(cacheDir) + (externalCacheDir?.let { dirSize(it) } ?: 0L)
                    cacheDir.listFiles()?.forEach { it.deleteRecursively() }
                    externalCacheDir?.listFiles()?.forEach { it.deleteRecursively() }
                    result.success(freed)
                }
                // Keeping the VPN alive in the background: battery optimization
                // and the maker (each brand has its own background settings).
                "background" -> {
                    val pm = getSystemService(PowerManager::class.java)
                    result.success(
                        mapOf(
                            "ignoring" to (pm?.isIgnoringBatteryOptimizations(packageName) == true),
                            "maker" to Build.MANUFACTURER.lowercase(),
                            "brand" to Build.BRAND.lowercase(),
                        )
                    )
                }
                "ignoreBatteryOptimizations" -> result.success(requestIgnoreBatteryOptimizations())
                "openAppSettings" -> result.success(
                    open(Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.parse("package:$packageName")))
                )
                else -> result.notImplemented()
            }
        }

        EventChannel(messenger, "vpn_platform/vpn_status").setStreamHandler(object : EventChannel.StreamHandler {
            override fun onListen(arguments: Any?, events: EventChannel.EventSink) {
                statusListener?.let { VpnState.Watcher.remove(it) }
                val l: (VpnState.Snapshot) -> Unit = { s ->
                    events.success(mapOf("status" to s.status, "message" to s.message, "since" to s.since))
                }
                statusListener = l
                VpnState.Watcher.add(this@MainActivity, l) // sends the current state right away
            }

            override fun onCancel(arguments: Any?) {
                statusListener?.let { VpnState.Watcher.remove(it) }
                statusListener = null
            }
        })
    }

    override fun cleanUpFlutterEngine(flutterEngine: FlutterEngine) {
        statusListener?.let { VpnState.Watcher.remove(it) }
        statusListener = null
        super.cleanUpFlutterEngine(flutterEngine)
    }

    // While the app is on screen, notice a VPN process that died without a
    // word (killed or crashed) within ~2 s.
    override fun onResume() {
        super.onResume()
        VpnState.Watcher.resume(this)
    }

    override fun onPause() {
        VpnState.Watcher.pause()
        super.onPause()
    }

    /** The system dialog "let 环游 run in the background"; the settings list if a ROM lacks it. */
    @SuppressLint("BatteryLife") // a VPN must keep running in the background
    private fun requestIgnoreBatteryOptimizations(): Boolean {
        val pm = getSystemService(PowerManager::class.java)
        if (pm?.isIgnoringBatteryOptimizations(packageName) == true) return true
        return open(Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, Uri.parse("package:$packageName"))) ||
            open(Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS))
    }

    private fun open(i: Intent): Boolean = try {
        startActivity(i)
        true
    } catch (_: Exception) {
        false
    }

    private fun connect(name: String, result: MethodChannel.Result) {
        val consent = VpnService.prepare(this)
        if (consent != null) {
            // A newer connect replaces one still waiting for the dialog: the
            // older call must still complete, or its Dart future hangs.
            pendingResult?.error("cancelled", null, null)
            pendingName = name
            pendingResult = result
            // Only the VPN dialog now; asking for the notification permission
            // at the same moment made some ROMs cancel the VPN dialog.
            startActivityForResult(consent, REQ_VPN) // system "allow VPN connection" dialog
        } else {
            askNotificationPermission()
            try {
                startVpnService(name)
            } catch (e: Exception) {
                // Android 12+: starting a foreground service from the
                // background is refused (ForegroundServiceStartNotAllowedException).
                result.error("start_failed", "请打开应用后再连接", null)
                return
            }
            result.success(null)
        }
    }

    /** Optional: lets the "connected" notification show. VPN works either way. */
    private fun askNotificationPermission() {
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), REQ_NOTIF)
        }
    }

    private fun startVpnService(name: String) {
        val i = Intent(this, XrayVpnService::class.java)
            .setAction(XrayVpnService.ACTION_CONNECT)
            .putExtra(XrayVpnService.EXTRA_NAME, name)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) startForegroundService(i) else startService(i)
    }

    @Deprecated("Deprecated in Java")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode != REQ_VPN) return
        val result = pendingResult ?: return
        pendingResult = null
        if (resultCode == Activity.RESULT_OK) {
            startVpnService(pendingName ?: "VPN")
            result.success(null)
            askNotificationPermission()
        } else {
            result.error("permission_denied", "需要允许 VPN 连接权限", null)
        }
    }

    private fun dirSize(f: java.io.File): Long =
        if (f.isFile) f.length() else (f.listFiles()?.sumOf { dirSize(it) } ?: 0L)
}
