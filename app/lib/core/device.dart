import 'dart:io';

import 'package:flutter/services.dart';

/// Bridge to a stable, per-device identity used for no-login authentication.
///
/// Android: Settings.Secure.ANDROID_ID — survives app reinstall.
/// iOS: not implemented natively; the caller falls back to a random id kept in
/// the Keychain (flutter_secure_storage), which also survives reinstall.
class Device {
  static const _ch = MethodChannel('vpn_platform/device');

  static Future<String?> nativeId() async {
    try {
      return await _ch.invokeMethod<String>('deviceId');
    } catch (_) {
      return null; // iOS / tests: fall back to a stored id
    }
  }

  static String get platform {
    if (Platform.isIOS) return 'ios';
    if (Platform.isAndroid) return 'android';
    return '';
  }
}
