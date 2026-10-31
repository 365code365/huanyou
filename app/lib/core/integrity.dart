import 'dart:async';
import 'dart:io';

import 'package:flutter/services.dart';

import 'device.dart';

/// Device / app integrity report, signed into every API request (X-HY-Env)
/// so the server can refuse repackaged apps and, if the admin wants, devices
/// running a debugger or a hooking framework (server: secure.go checkEnv).
///
/// Android: the signing certificate's SHA-256, debuggable flag / attached
/// debugger, Frida / Xposed traces, root. iOS: debugger, injected libraries
/// (Frida, Substrate …), jailbreak. These checks raise the bar; the decision
/// is the server's, because a determined attacker can patch the app.
class Integrity {
  static const _ch = MethodChannel('vpn_platform/device');

  /// SHA-256 of the official Android signing certificate, set by
  /// scripts/build_android.sh (empty in other builds: not checked locally).
  static const expectedSigner = String.fromEnvironment('APP_SIGNER');

  /// The X-HY-Env value ("" before [check]).
  static String env = '';

  /// The installed Android app is signed by someone else (repackaged).
  static bool tampered = false;

  static Future<void> check() async {
    final p = Device.platform;
    if (!Platform.isAndroid && !Platform.isIOS) {
      env = 'p=$p';
      return;
    }
    try {
      final m = Map<String, dynamic>.from(
          await _ch.invokeMethod<Map<Object?, Object?>>('integrity').timeout(const Duration(seconds: 3)) ?? {});
      String b(String k) => m[k] == true ? '1' : '0';
      final sig = (m['sig'] as String? ?? '').toLowerCase();
      env = 'p=$p;sig=$sig;dbg=${b('dbg')};hook=${b('hook')};root=${b('root')}';
      tampered = Platform.isAndroid && expectedSigner.isNotEmpty && sig.isNotEmpty && sig != expectedSigner.toLowerCase();
    } catch (_) {
      env = 'p=$p;chk=0'; // the check itself failed (reported, not blocked)
    }
  }
}
