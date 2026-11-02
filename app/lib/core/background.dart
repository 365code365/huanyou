import 'dart:io';

import 'package:flutter/services.dart';

/// Android: what lets the VPN keep running in the background. Many phones
/// (OPPO / OnePlus / realme, Xiaomi, Huawei / Honor, vivo …) close apps in
/// the background to save power, a running VPN included, unless the user
/// allows it in the system settings.
class BackgroundRun {
  BackgroundRun._();

  static const _ch = MethodChannel('vpn_platform/device');

  /// null off Android (or when the native side is missing).
  static Future<BackgroundState?> state() async {
    if (!Platform.isAndroid) return null;
    try {
      final m = await _ch.invokeMapMethod<String, dynamic>('background');
      if (m == null) return null;
      return BackgroundState(
        ignoringBattery: m['ignoring'] == true,
        maker: '${m['maker'] ?? ''}',
        brand: '${m['brand'] ?? ''}',
      );
    } catch (_) {
      return null;
    }
  }

  /// The system dialog that exempts the app from battery optimization.
  static Future<void> requestIgnoreBattery() async {
    try {
      await _ch.invokeMethod<bool>('ignoreBatteryOptimizations');
    } catch (_) {}
  }

  /// The app's page in the system settings (battery / auto-start live there).
  static Future<void> openAppSettings() async {
    try {
      await _ch.invokeMethod<bool>('openAppSettings');
    } catch (_) {}
  }
}

/// The phone's own background manager, by maker.
enum PhoneSystem { coloros, miui, huawei, vivo, samsung, other }

class BackgroundState {
  const BackgroundState({required this.ignoringBattery, required this.maker, required this.brand});

  /// Exempt from the system's battery optimization.
  final bool ignoringBattery;
  final String maker, brand;

  PhoneSystem get system {
    for (final m in [maker, brand]) {
      switch (m) {
        case 'oppo' || 'oneplus' || 'realme':
          return PhoneSystem.coloros;
        case 'xiaomi' || 'redmi' || 'poco':
          return PhoneSystem.miui;
        case 'huawei' || 'honor':
          return PhoneSystem.huawei;
        case 'vivo' || 'iqoo':
          return PhoneSystem.vivo;
        case 'samsung':
          return PhoneSystem.samsung;
      }
    }
    return PhoneSystem.other;
  }
}
