import 'dart:async';
import 'dart:io';

import 'package:flutter/services.dart';

import 'vpn_desktop.dart';

enum VpnStatus { disconnected, connecting, connected, disconnecting, error }

/// Why the VPN stopped while the app was away (Android).
class VpnInterruption {
  const VpnInterruption(this.reason, this.detail);

  /// system (the phone's background cleanup), lowmem, crash, update (the
  /// app was updated), reboot, user (force-stopped in the system settings).
  final String reason;

  /// The system's exit record, for crash reports.
  final String? detail;

  /// The phone closed it to save power / memory: allowing background
  /// running prevents it.
  bool get bySystem => reason == 'system' || reason == 'lowmem';
}

/// Bridge to the native VPN implementation
/// (Android: XrayVpnService, iOS: PacketTunnel extension,
/// Windows / macOS: huanyou-core in system-proxy mode, see vpn_desktop.dart).
class Vpn {
  static const _method = MethodChannel('vpn_platform/vpn');
  static const _events = EventChannel('vpn_platform/vpn_status');
  static bool get _desktop => Platform.isWindows || Platform.isMacOS;

  /// Status pushed by the native side. Error events carry a message.
  static Stream<(VpnStatus, String?)> statusStream() {
    if (_desktop) return DesktopVpn.statusStream();
    return _events.receiveBroadcastStream().map((e) {
      final m = Map<String, dynamic>.from(e as Map);
      final since = (m['since'] as num?)?.toInt() ?? 0;
      connectedSince = since > 0 ? DateTime.fromMillisecondsSinceEpoch(since) : null;
      return (_parse(m['status'] as String?), m['message'] as String?);
    });
  }

  /// When the running tunnel first connected, as reported by the native side
  /// (survives the app being closed and reopened while the VPN stays up).
  static DateTime? connectedSince;

  static Future<VpnStatus> status() async {
    if (_desktop) return DesktopVpn.status();
    return _parse(await _method.invokeMethod<String>('status'));
  }

  /// Asks for VPN permission if needed and starts the tunnel with [config]
  /// (compiled profile from the API). Throws PlatformException on failure.
  static Future<void> connect(String config, {required String name}) {
    if (_desktop) return DesktopVpn.connect(config, name: name);
    return _method.invokeMethod('connect', {'config': config, 'name': name});
  }

  /// Desktop can switch node / mode inside the running tunnel (no
  /// disconnect, the system proxy stays as it is).
  static bool get canSwitchLive => _desktop;

  static Future<void> disconnect() {
    if (_desktop) return DesktopVpn.disconnect();
    return _method.invokeMethod('disconnect');
  }

  /// Android: the tunnel stopped since the last run without the user turning
  /// it off (see VpnInterruption). Reported once; null if it didn't.
  static Future<VpnInterruption?> takeInterruption() async {
    if (!Platform.isAndroid) return null;
    try {
      final m = await _method.invokeMapMethod<String, dynamic>('takeInterruption');
      if (m == null) return null;
      return VpnInterruption(m['reason'] as String? ?? 'system', m['detail'] as String?);
    } catch (_) {
      return null;
    }
  }

  static VpnStatus _parse(String? s) => switch (s) {
        'connecting' => VpnStatus.connecting,
        'connected' => VpnStatus.connected,
        'disconnecting' => VpnStatus.disconnecting,
        'error' => VpnStatus.error,
        _ => VpnStatus.disconnected,
      };
}
