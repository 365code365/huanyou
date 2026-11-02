package com.vpnplatform.vpn_app

import android.app.ActivityManager
import android.app.ApplicationExitInfo
import android.content.Context
import android.os.Build
import android.os.SystemClock
import java.io.File

/**
 * Tells the app at its next start that the tunnel stopped without the user
 * turning it off, and why.
 *
 * The VPN process keeps a marker file while the VPN is meant to be on; the
 * user's own stop (or another VPN taking over) removes it. A marker with no
 * tunnel running means the process was killed (the phone's background
 * cleanup, low memory), crashed, the app was updated or the phone restarted.
 * The system's process exit records tell which.
 */
object Interruption {
    private fun marker(c: Context) = File(c.noBackupFilesDir, "vpn_on")

    /** VPN process: the VPN is on (or being brought back) from now on. */
    fun on(c: Context) {
        try {
            marker(c).writeText(System.currentTimeMillis().toString())
        } catch (_: Exception) {
        }
    }

    /** VPN process: stopped on purpose (the user, another VPN app, a final failure). */
    fun off(c: Context) {
        marker(c).delete()
    }

    /**
     * UI process: {reason, detail} once after an interruption, else null.
     * reason: system, lowmem, crash, update, reboot, user (force-stopped in
     * the system settings: the user's choice).
     */
    fun take(c: Context): Map<String, Any?>? {
        val f = marker(c)
        if (!f.exists()) return null
        if (VpnState.read(c).active) return null // running (or the system already restarted it)
        val since = try {
            f.readText().trim().toLong()
        } catch (_: Exception) {
            f.lastModified()
        }
        f.delete()
        val (reason, detail) = classify(c, since)
        return mapOf("reason" to reason, "detail" to detail)
    }

    private fun classify(c: Context, since: Long): Pair<String, String?> {
        val bootedAt = System.currentTimeMillis() - SystemClock.elapsedRealtime()
        if (since < bootedAt) return "reboot" to null
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.R) return "system" to null
        val name = c.packageName + VpnState.PROCESS_SUFFIX
        val e = try {
            c.getSystemService(ActivityManager::class.java)
                ?.getHistoricalProcessExitReasons(c.packageName, 0, 16)
                ?.firstOrNull { it.processName == name && it.timestamp >= since }
        } catch (_: Exception) {
            null
        } ?: return "system" to null
        val reason = when (e.reason) {
            // Before Android 14 an update is a "user requested" stop "due to installPackageLI".
            ApplicationExitInfo.REASON_USER_REQUESTED, ApplicationExitInfo.REASON_USER_STOPPED ->
                if (e.description?.contains("install", ignoreCase = true) == true) "update" else "user"
            // REASON_PACKAGE_STATE_CHANGE / REASON_PACKAGE_UPDATED (API 34)
            15, 16 -> "update"
            ApplicationExitInfo.REASON_LOW_MEMORY -> "lowmem"
            ApplicationExitInfo.REASON_CRASH, ApplicationExitInfo.REASON_CRASH_NATIVE,
            ApplicationExitInfo.REASON_ANR, ApplicationExitInfo.REASON_INITIALIZATION_FAILURE -> "crash"
            else -> "system" // incl. vendor kills ("o-stop", reason OTHER)
        }
        return reason to "reason=${e.reason} ${e.description ?: ""}".trim()
    }
}
