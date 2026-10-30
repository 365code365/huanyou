import 'dart:async';
import 'dart:io';

import 'package:flutter/services.dart';

import 'vpn_desktop.dart';

enum VpnStatus { disconnected, connecting, connected, disconnecting, error }

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
      return (_parse(m['status'] as String?), m['message'] as String?);
    });
  }

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

  static VpnStatus _parse(String? s) => switch (s) {
        'connecting' => VpnStatus.connecting,
        'connected' => VpnStatus.connected,
        'disconnecting' => VpnStatus.disconnecting,
        'error' => VpnStatus.error,
        _ => VpnStatus.disconnected,
      };
}
