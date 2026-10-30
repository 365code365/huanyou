package com.vpnplatform.vpn_app

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
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

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        val messenger = flutterEngine.dartExecutor.binaryMessenger

        MethodChannel(messenger, "vpn_platform/vpn").setMethodCallHandler { call, result ->
            when (call.method) {
                "status" -> result.success(VpnState.status)
                "connect" -> {
                    val config = call.argument<String>("config")
                    if (config.isNullOrEmpty()) {
                        result.error("bad_args", "缺少配置", null)
                        return@setMethodCallHandler
                    }
                    XrayVpnService.profileFile(this).writeText(config)
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
                else -> result.notImplemented()
            }
        }

        EventChannel(messenger, "vpn_platform/vpn_status").setStreamHandler(object : EventChannel.StreamHandler {
            private var listener: ((String, String?) -> Unit)? = null
            override fun onListen(arguments: Any?, events: EventChannel.EventSink) {
                val l: (String, String?) -> Unit = { s, m -> events.success(mapOf("status" to s, "message" to m)) }
                listener = l
                VpnState.addListener(l)
                l(VpnState.status, VpnState.message)
            }

            override fun onCancel(arguments: Any?) {
                listener?.let { VpnState.removeListener(it) }
                listener = null
            }
        })
    }

    private fun connect(name: String, result: MethodChannel.Result) {
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            // Optional: lets the "connected" notification show. VPN works either way.
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), REQ_NOTIF)
        }
        val consent = VpnService.prepare(this)
        if (consent != null) {
            pendingName = name
            pendingResult = result
            startActivityForResult(consent, REQ_VPN) // system "allow VPN connection" dialog
        } else {
            startVpnService(name)
            result.success(null)
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
        } else {
            result.error("permission_denied", "需要允许 VPN 连接权限", null)
        }
    }
}
