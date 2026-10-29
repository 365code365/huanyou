package com.vpnplatform.vpn_app

import android.os.Handler
import android.os.Looper

/** Process-wide VPN status, observed by the Flutter EventChannel. */
object VpnState {
    @Volatile var status: String = "disconnected"
        private set
    @Volatile var message: String? = null
        private set

    private val main = Handler(Looper.getMainLooper())
    private val listeners = mutableSetOf<(String, String?) -> Unit>()

    fun set(status: String, message: String? = null) {
        this.status = status
        this.message = message
        main.post { listeners.toList().forEach { it(status, message) } }
    }

    fun addListener(l: (String, String?) -> Unit) = main.post { listeners.add(l) }
    fun removeListener(l: (String, String?) -> Unit) = main.post { listeners.remove(l) }
}
