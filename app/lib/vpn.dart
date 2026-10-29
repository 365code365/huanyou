import 'dart:async';

import 'package:flutter/services.dart';

enum VpnStatus { disconnected, connecting, connected, disconnecting, error }

/// Bridge to the native VPN implementation
/// (Android: XrayVpnService, iOS: PacketTunnel extension).
class Vpn {
  static const _method = MethodChannel('vpn_platform/vpn');
  static const _events = EventChannel('vpn_platform/vpn_status');

  /// Status pushed by the native side. Error events carry a message.
  static Stream<(VpnStatus, String?)> statusStream() =>
      _events.receiveBroadcastStream().map((e) {
        final m = Map<String, dynamic>.from(e as Map);
        return (_parse(m['status'] as String?), m['message'] as String?);
      });

  static Future<VpnStatus> status() async => _parse(await _method.invokeMethod<String>('status'));

  /// Asks for VPN permission if needed and starts the tunnel with [config]
  /// (Xray JSON from the API). Throws PlatformException on failure.
  static Future<void> connect(String config, {required String name}) =>
      _method.invokeMethod('connect', {'config': config, 'name': name});

  static Future<void> disconnect() => _method.invokeMethod('disconnect');

  static VpnStatus _parse(String? s) => switch (s) {
        'connecting' => VpnStatus.connecting,
        'connected' => VpnStatus.connected,
        'disconnecting' => VpnStatus.disconnecting,
        'error' => VpnStatus.error,
        _ => VpnStatus.disconnected,
      };
}
