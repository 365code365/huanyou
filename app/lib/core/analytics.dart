import 'dart:async';
import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';
import 'package:package_info_plus/package_info_plus.dart';

import 'api.dart';
import 'device.dart';
import 'prefs.dart';
import 'session.dart';

/// Usage statistics and crash reports.
///
/// Events are names from a fixed list (app_open, vpn_connected, plans_view …)
/// with a count; nothing about what the user visits is collected. They are
/// batched in memory and sent every 30 s and when the app goes to the
/// background. Crash reports (Dart / Flutter errors, and native crashes
/// saved by the Android side) can be turned off in 我的 → 设置.
class Analytics {
  Analytics._();

  static const _kCrash = 'crash_report';
  static final Map<String, int> _pending = {};
  static ApiClient? _api;
  static Session? _session;
  static String _version = '';
  static Timer? _timer;
  static int _sentThisMinute = 0;
  static int _minute = 0;

  static bool get crashReportsOn => Prefs.get(_kCrash) != '0';
  static Future<void> setCrashReports(bool on) => Prefs.set(_kCrash, on ? '1' : '0');

  static Future<void> init(ApiClient api, Session session) async {
    _api = api;
    _session = session;
    try {
      _version = (await PackageInfo.fromPlatform()).version;
    } catch (_) {}
    _timer ??= Timer.periodic(const Duration(seconds: 30), (_) => flush());
    unawaited(_sendNativeCrash());
  }

  /// Counts one occurrence of an event (sent with the next batch).
  static void track(String event) {
    _pending[event] = (_pending[event] ?? 0) + 1;
  }

  static Future<void> flush() async {
    final api = _api;
    if (api == null || _pending.isEmpty) return;
    final batch = Map.of(_pending);
    _pending.clear();
    try {
      await api.post('/api/v1/events', {
        'device_id': await _session!.deviceId(),
        'platform': Device.platform,
        'version': _version,
        'events': [for (final e in batch.entries) {'name': e.key, 'count': e.value}],
      });
    } catch (_) {
      // Offline: put them back for the next round (bounded).
      if (_pending.length < 100) {
        batch.forEach((k, v) => _pending[k] = (_pending[k] ?? 0) + v);
      }
    }
  }

  /// Reports an error / crash if the user allows it. At most 10 per minute so
  /// an error in a build() loop can't flood the server.
  static Future<void> reportError(Object error, StackTrace? stack, {String kind = 'dart'}) async {
    if (kDebugMode) debugPrint('[$kind] $error\n$stack');
    if (!crashReportsOn || _api == null) return;
    final m = DateTime.now().millisecondsSinceEpoch ~/ 60000;
    if (m != _minute) {
      _minute = m;
      _sentThisMinute = 0;
    }
    if (++_sentThisMinute > 10) return;
    await _send(kind, error.toString(), stack?.toString() ?? '');
  }

  static Future<void> _send(String kind, String message, String stack) async {
    try {
      await _api!.post('/api/v1/crash', {
        'device_id': await _session!.deviceId(),
        'platform': Device.platform,
        'version': _version,
        'device': await Device.model(),
        'os_version': Platform.operatingSystemVersion,
        'kind': kind,
        'message': message.length > 1500 ? message.substring(0, 1500) : message,
        'stack': stack.length > 16000 ? stack.substring(0, 16000) : stack,
      });
    } catch (_) {
      // Best effort only.
    }
  }

  /// A native (Kotlin) crash is written to a file by the crashed process;
  /// send it on the next start.
  static Future<void> _sendNativeCrash() async {
    if (!Platform.isAndroid) return;
    try {
      final text = await const MethodChannel('vpn_platform/device').invokeMethod<String>('takeCrash');
      if (text == null || text.isEmpty || !crashReportsOn) return;
      final nl = text.indexOf('\n');
      await _send('native', nl > 0 ? text.substring(0, nl) : text, nl > 0 ? text.substring(nl + 1) : '');
    } catch (_) {}
  }
}
