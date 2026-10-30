import 'dart:ffi' show Abi;
import 'dart:io';

import 'package:flutter/services.dart';

/// Bridge to a stable, per-device identity used for no-login authentication.
///
/// Android: Settings.Secure.ANDROID_ID — survives app reinstall.
/// Windows: the MachineGuid Windows creates at install time — survives app
/// reinstall, changes when Windows is reinstalled.
/// macOS: the hardware IOPlatformUUID.
/// iOS: not implemented natively; the caller falls back to a random id kept in
/// the Keychain (flutter_secure_storage), which also survives reinstall.
class Device {
  static const _ch = MethodChannel('vpn_platform/device');

  static Future<String?> nativeId() async {
    if (Platform.isWindows) return _windowsMachineGuid();
    if (Platform.isMacOS) return _macPlatformUuid();
    try {
      return await _ch.invokeMethod<String>('deviceId');
    } catch (_) {
      return null; // iOS / tests: fall back to a stored id
    }
  }

  static Future<String?> _windowsMachineGuid() async {
    try {
      final r = await Process.run(
        'reg',
        ['query', r'HKLM\SOFTWARE\Microsoft\Cryptography', '/v', 'MachineGuid', '/reg:64'],
      ).timeout(const Duration(seconds: 5));
      final m = RegExp(r'MachineGuid\s+REG_SZ\s+([0-9A-Fa-f-]{36})').firstMatch('${r.stdout}');
      return m == null ? null : 'win-${m.group(1)!.toLowerCase()}';
    } catch (_) {
      return null;
    }
  }

  static Future<String?> _macPlatformUuid() async {
    try {
      final r = await Process.run('/usr/sbin/ioreg', ['-rd1', '-c', 'IOPlatformExpertDevice']).timeout(const Duration(seconds: 5));
      final m = RegExp(r'"IOPlatformUUID"\s*=\s*"([0-9A-Fa-f-]{36})"').firstMatch('${r.stdout}');
      return m == null ? null : 'mac-${m.group(1)!.toLowerCase()}';
    } catch (_) {
      return null;
    }
  }

  /// CPU type for update checks: the primary ABI on Android ("arm64-v8a",
  /// "armeabi-v7a", …), "arm64" / "x86_64" on desktop, "" on iOS.
  static Future<String> abi() async {
    if (Platform.isWindows || Platform.isMacOS) {
      return switch (Abi.current()) {
        Abi.windowsArm64 || Abi.macosArm64 => 'arm64',
        Abi.windowsX64 || Abi.macosX64 => 'x86_64',
        _ => '',
      };
    }
    if (!Platform.isAndroid) return '';
    try {
      return await _ch.invokeMethod<String>('abi') ?? '';
    } catch (_) {
      return '';
    }
  }

  /// Phone model, e.g. "OPPO PLY110" (crash reports).
  static Future<String> model() async {
    if (Platform.isWindows) return 'Windows PC';
    if (Platform.isMacOS) return 'Mac';
    try {
      return await _ch.invokeMethod<String>('model') ?? '';
    } catch (_) {
      return Platform.isIOS ? 'iPhone' : '';
    }
  }

  static String get platform {
    if (Platform.isIOS) return 'ios';
    if (Platform.isAndroid) return 'android';
    if (Platform.isWindows) return 'windows';
    if (Platform.isMacOS) return 'macos';
    return '';
  }
}
