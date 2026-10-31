package com.vpnplatform.vpn_app

import android.os.Handler
import android.os.Looper

/** Process-wide VPN status, observed by the Flutter EventChannel. */
object VpnState {
    @Volatile var status: String = "disconnected"
        private set
    @Volatile var message: String? = null
        private set
    /** When the current session first connected (ms), kept across rebuilds on
     *  network changes; 0 when down. Lets a re-created app show the real
     *  connected time instead of starting from zero. */
    @Volatile var since: Long = 0L
        private set

    private val main = Handler(Looper.getMainLooper())
    private val listeners = mutableSetOf<(String, String?) -> Unit>()

    fun set(status: String, message: String? = null) {
        if (status == "connected" && since == 0L) since = System.currentTimeMillis()
        if (status == "disconnected" || status == "error") since = 0L
        this.status = status
        this.message = message
        main.post { listeners.toList().forEach { it(status, message) } }
    }

    fun addListener(l: (String, String?) -> Unit) = main.post { listeners.add(l) }
    fun removeListener(l: (String, String?) -> Unit) = main.post { listeners.remove(l) }
}
