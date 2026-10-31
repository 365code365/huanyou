# R8 rules for the release build (shrinking + obfuscation).

# Xray core (gomobile): the Go side calls these by name through JNI.
-keep class go.** { *; }
-keep class com.vpnplatform.vpncore.** { *; }
-keep interface com.vpnplatform.vpncore.** { *; }

# VpnService / activity entry points are kept by the manifest; nothing else
# of ours is called by name.

# Flutter references Play Core (deferred components), which this app does not ship.
-dontwarn com.google.android.play.core.**

# Strip verbose / debug logging from release code.
-assumenosideeffects class android.util.Log {
    public static int v(...);
    public static int d(...);
}
