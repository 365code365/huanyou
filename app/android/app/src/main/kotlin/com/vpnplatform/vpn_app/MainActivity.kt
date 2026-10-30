package com.vpnplatform.vpn_app

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.net.Uri
import android.provider.Settings
import androidx.core.content.FileProvider
import io.flutter.embedding.android.FlutterActivity
import java.io.File
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

        // In-app update: open the system installer for a downloaded APK.
        MethodChannel(messenger, "vpn_platform/update").setMethodCallHandler { call, result ->
            when (call.method) {
                "installApk" -> {
                    val path = call.argument<String>("path")
                    if (path == null) {
                        result.error("bad_args", "缺少安装包路径", null)
                        return@setMethodCallHandler
                    }
                    installApk(File(path), result)
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

    private fun installApk(file: File, result: MethodChannel.Result) {
        if (!file.exists()) {
            result.error("missing", "安装包不存在，请重新下载", null)
            return
        }
        // Android 8+: the user must allow this app to install packages, once.
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O && !packageManager.canRequestPackageInstalls()) {
            startActivity(
                Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:$packageName"))
                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            )
            result.error("permission", "请在打开的设置里允许「安装未知应用」，然后返回点「重试」", null)
            return
        }
        val uri = FileProvider.getUriForFile(this, "$packageName.updates", file)
        val intent = Intent(Intent.ACTION_VIEW)
            .setDataAndType(uri, "application/vnd.android.package-archive")
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
        try {
            startActivity(intent)
            result.success(null)
        } catch (e: Exception) {
            result.error("install", "无法打开系统安装器：${e.message}", null)
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
