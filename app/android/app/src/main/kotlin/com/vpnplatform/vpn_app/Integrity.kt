package com.vpnplatform.vpn_app

import android.content.Context
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.content.pm.Signature
import android.os.Build
import android.os.Debug
import java.io.File
import java.net.InetSocketAddress
import java.net.Socket
import java.security.MessageDigest

/**
 * Integrity report for the API (lib/core/integrity.dart → X-HY-Env).
 *
 * sig:  SHA-256 of the signing certificate (a repackaged app has another one)
 * dbg:  debuggable build / debugger attached
 * hook: Frida (agent in memory, its threads, frida-server port) or Xposed /
 *       LSPosed loaded into this process
 * root: su / Magisk present (reported only; many normal users root)
 *
 * Runs off the main thread (it reads /proc and probes a local port).
 */
object Integrity {
    fun check(ctx: Context): Map<String, Any> = mapOf(
        "sig" to signer(ctx),
        "dbg" to debugged(ctx),
        "hook" to hooked(),
        "root" to rooted(),
    )

    private fun signer(ctx: Context): String = try {
        val pm = ctx.packageManager
        val certs: Array<Signature>? = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            pm.getPackageInfo(ctx.packageName, PackageManager.GET_SIGNING_CERTIFICATES).signingInfo?.apkContentsSigners
        } else {
            @Suppress("DEPRECATION")
            pm.getPackageInfo(ctx.packageName, PackageManager.GET_SIGNATURES).signatures
        }
        val c = certs?.firstOrNull()
        if (c == null) "" else MessageDigest.getInstance("SHA-256").digest(c.toByteArray()).joinToString("") { "%02x".format(it) }
    } catch (_: Exception) {
        ""
    }

    private fun debugged(ctx: Context): Boolean =
        Debug.isDebuggerConnected() || Debug.waitingForDebugger() ||
            (ctx.applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE) != 0

    private val hookLibs = listOf("frida", "gum-js", "gadget", "xposed", "lspatch", "lsposed", "substrate", "edxp", "riru")

    private fun hooked(): Boolean {
        // Libraries mapped into this process.
        try {
            File("/proc/self/maps").useLines { lines ->
                if (lines.any { l -> val s = l.lowercase(); hookLibs.any { s.contains(it) } }) return true
            }
        } catch (_: Exception) {
        }
        // Frida's own threads.
        try {
            File("/proc/self/task").listFiles()?.forEach { t ->
                val name = try { File(t, "comm").readText().trim() } catch (_: Exception) { "" }
                if (name == "gum-js-loop" || name == "gmain" && hasFridaPipe() || name.startsWith("pool-frida")) return true
            }
        } catch (_: Exception) {
        }
        // Xposed / LSPosed hooks in this process.
        try {
            Class.forName("de.robv.android.xposed.XposedBridge")
            return true
        } catch (_: Throwable) {
        }
        if (Throwable().stackTrace.any { it.className.contains("xposed", true) || it.className.contains("LSPHooker") }) return true
        // frida-server's default port on this device.
        return try {
            Socket().use { it.connect(InetSocketAddress("127.0.0.1", 27042), 150); true }
        } catch (_: Exception) {
            false
        }
    }

    private fun hasFridaPipe(): Boolean = try {
        File("/proc/self/fd").listFiles()?.any { f ->
            try { f.canonicalPath.contains("linjector") || f.canonicalPath.contains("frida") } catch (_: Exception) { false }
        } == true
    } catch (_: Exception) {
        false
    }

    private fun rooted(): Boolean {
        val paths = listOf(
            "/system/bin/su", "/system/xbin/su", "/sbin/su", "/su/bin/su", "/data/local/xbin/su",
            "/data/local/bin/su", "/system/sd/xbin/su", "/data/adb/magisk", "/data/adb/ksu",
        )
        return paths.any { File(it).exists() } || Build.TAGS?.contains("test-keys") == true
    }
}
