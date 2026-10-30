package com.vpnplatform.vpn_app

import android.content.Context
import java.io.File
import java.io.PrintWriter
import java.io.StringWriter

/**
 * Saves an uncaught JVM exception (Activity or VPN service) to a file before
 * the process dies; the Flutter side reads and reports it on the next start.
 */
object CrashCatcher {
    private const val FILE = "last_crash.txt"
    @Volatile private var installed = false

    fun install(ctx: Context) {
        if (installed) return
        installed = true
        val app = ctx.applicationContext
        val previous = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, e ->
            try {
                val sw = StringWriter()
                e.printStackTrace(PrintWriter(sw))
                File(app.filesDir, FILE).writeText("${e.javaClass.name}: ${e.message} (thread ${thread.name})\n$sw")
            } catch (_: Throwable) {
            }
            previous?.uncaughtException(thread, e)
        }
    }

    fun take(ctx: Context): String? {
        val f = File(ctx.filesDir, FILE)
        if (!f.exists()) return null
        return try { f.readText().also { f.delete() } } catch (_: Throwable) { null }
    }
}
