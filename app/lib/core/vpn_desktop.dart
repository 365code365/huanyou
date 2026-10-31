import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

import '../i18n/i18n.dart';
import 'vpn.dart';

/// Windows and macOS: runs huanyou-core (built from mobile/cmd/huanyou-core;
/// next to the app's exe on Windows, in 环游.app/Contents/MacOS on macOS) in
/// system-proxy mode. Protocol: the profile goes
/// in as the first stdin line; the core prints one JSON status line per
/// change; another stdin line or EOF stops it and restores the user's proxy.
/// EOF also covers the app being closed or crashing.
class DesktopVpn {
  static final _events = StreamController<(VpnStatus, String?)>.broadcast();
  static Process? _proc;
  // Exit of the last core: after reporting "disconnected" it may still be
  // restoring the proxy of other network services, and holds a lock until
  // then, so the next connect waits for it.
  static Future<int>? _lastExit;
  static Completer<void>? _stopped;
  // disconnect() was called before the core reported "connected".
  static bool _cancelled = false;
  static VpnStatus _status = VpnStatus.disconnected;
  static bool _restored = false;

  /// Path of the core exe; tests point it at a locally built binary.
  @visibleForTesting
  static String? corePath;

  static String get _core {
    if (corePath != null) return corePath!;
    final dir = File(Platform.resolvedExecutable).parent.path;
    return Platform.isWindows ? '$dir\\huanyou-core.exe' : '$dir/huanyou-core';
  }

  static Stream<(VpnStatus, String?)> statusStream() => _events.stream;

  // Crash recovery in progress (see status); connect waits for it.
  static Future<void>? _restoring;

  /// The proxy backup the core keeps while it has changed the system proxy
  /// (mobile/cmd/huanyou-core/backup.go: the user cache dir + Huanyou).
  static File get _backup {
    final env = Platform.environment;
    final dir = Platform.isWindows
        ? '${env['LOCALAPPDATA'] ?? ''}\\Huanyou'
        : '${env['HOME'] ?? ''}/Library/Caches/Huanyou';
    return File(Platform.isWindows ? '$dir\\proxy-backup.json' : '$dir/proxy-backup.json');
  }

  static Future<VpnStatus> status() async {
    if (!_restored && _proc == null) {
      _restored = true;
      // A core killed last time (Task Manager, power loss) left the system
      // proxy pointing at a closed port: put the user's setting back. Only
      // when there is something to undo, and without holding up app start.
      if (_backup.existsSync()) {
        _restoring = Process.run(_core, ['restore'])
            .timeout(const Duration(seconds: 15))
            .then<void>((_) {}, onError: (_) {})
            .whenComplete(() => _restoring = null);
      }
    }
    return _status;
  }

  static void _set(VpnStatus s, [String? message]) {
    _status = s;
    _events.add((s, message));
  }

  static Future<void> connect(String config, {required String name}) async {
    final live = _proc;
    if (live != null && _status == VpnStatus.connected) {
      // Another node or mode while connected: the core swaps the profile on
      // the same local port, the system proxy stays untouched.
      final done = Completer<void>();
      final sub = _events.stream.listen((e) {
        if (done.isCompleted) return;
        if (e.$1 == VpnStatus.connected) done.complete();
        if (e.$1 == VpnStatus.error || e.$1 == VpnStatus.disconnected) {
          done.completeError(PlatformException(code: 'switch_failed', message: e.$2 ?? tr('无法启动 VPN')));
        }
      });
      try {
        live.stdin.writeln('reload ${config.trim()}');
        await live.stdin.flush();
        await done.future.timeout(const Duration(seconds: 15));
        return;
      } on TimeoutException {
        throw PlatformException(code: 'start_failed', message: tr('启动超时，请重试'));
      } finally {
        await sub.cancel();
      }
    }
    if (_proc != null) await disconnect();
    try {
      await _restoring;
      await _lastExit?.timeout(const Duration(seconds: 10));
    } catch (_) {}
    if (!File(_core).existsSync()) {
      throw PlatformException(code: 'no_core', message: tr('缺少连接组件 huanyou-core，请重新安装'));
    }
    final Process p;
    try {
      p = await Process.start(_core, ['run']);
    } on ProcessException catch (e) {
      throw PlatformException(code: 'start_failed', message: e.message);
    }
    _proc = p;
    _cancelled = false;
    _lastExit = p.exitCode;
    final stopped = _stopped = Completer<void>();
    _set(VpnStatus.connecting);
    final ready = Completer<void>();
    String? lastError;
    var reportedStop = false; // the core said "disconnected" (system proxy restored by it)

    // Xray's own log also goes to stdout; status lines are the JSON ones.
    p.stdout.transform(utf8.decoder).transform(const LineSplitter()).listen((line) {
      if (!line.startsWith('{')) return;
      final Map<String, dynamic> m;
      try {
        m = jsonDecode(line) as Map<String, dynamic>;
      } catch (_) {
        return;
      }
      final message = m['message'] as String?;
      switch (m['status']) {
        case 'connected':
          if (!ready.isCompleted) ready.complete();
          _set(VpnStatus.connected);
        case 'connecting':
          // Only reloads report this after start (switching node / mode).
          if (ready.isCompleted) _set(VpnStatus.connecting);
        case 'disconnecting':
          _set(VpnStatus.disconnecting);
        case 'disconnected':
          reportedStop = true;
          if (!ready.isCompleted) {
            // Stopped while starting (cancelled).
            ready.completeError(
              _cancelled
                  ? PlatformException(code: 'cancelled')
                  : PlatformException(code: 'start_failed', message: lastError ?? tr('无法启动 VPN')),
            );
          }
          // Traffic is direct again; the core finishes up in the background.
          if (identical(_proc, p) && lastError == null) {
            _proc = null;
            _set(VpnStatus.disconnected);
          }
          if (!stopped.isCompleted) stopped.complete();
        case 'error':
          lastError = message;
      }
    }, onError: (_) {});
    p.stderr.drain<void>().ignore();
    p.exitCode.then((_) {
      if (!stopped.isCompleted) stopped.complete();
      if (!identical(_proc, p)) {
        if (!ready.isCompleted) ready.completeError(PlatformException(code: 'cancelled'));
        return;
      }
      _proc = null;
      if (ready.isCompleted && !reportedStop) {
        // The core died while connected (crash, killed): it never got to undo
        // the system proxy, which now points at a dead port and leaves the
        // whole computer offline. Undo it now; a reconnect waits for this.
        _restoring = Process.run(_core, ['restore'])
            .timeout(const Duration(seconds: 15))
            .then<void>((_) {}, onError: (_) {})
            .whenComplete(() => _restoring = null);
      }
      if (!ready.isCompleted) {
        _status = VpnStatus.disconnected; // the caller reports the failure
        ready.completeError(
          _cancelled
              ? PlatformException(code: 'cancelled')
              : PlatformException(code: 'start_failed', message: lastError ?? tr('无法启动 VPN')),
        );
      } else if (lastError != null) {
        _set(VpnStatus.error, lastError);
      } else {
        _set(VpnStatus.disconnected);
      }
    });

    p.stdin.writeln(config.trim());
    await p.stdin.flush();
    try {
      await ready.future.timeout(const Duration(seconds: 20));
    } on TimeoutException {
      if (_cancelled) throw PlatformException(code: 'cancelled');
      _proc = null;
      p.kill();
      _status = VpnStatus.disconnected;
      await Process.run(_core, ['restore']).catchError((_) => ProcessResult(0, 0, '', ''));
      throw PlatformException(code: 'start_failed', message: tr('启动超时，请重试'));
    }
  }

  static Future<void> disconnect() async {
    final p = _proc;
    if (p == null) {
      _set(VpnStatus.disconnected);
      return;
    }
    if (_status == VpnStatus.connecting) _cancelled = true;
    _set(VpnStatus.disconnecting);
    try {
      p.stdin.writeln('stop');
      await p.stdin.flush();
      await p.stdin.close();
    } catch (_) {}
    try {
      // Done when the core reports "disconnected" (or exits).
      await (_stopped?.future ?? p.exitCode).timeout(const Duration(seconds: 8));
    } on TimeoutException {
      p.kill();
      await Process.run(_core, ['restore']).catchError((_) => ProcessResult(0, 0, '', ''));
      if (identical(_proc, p)) {
        _proc = null;
        _set(VpnStatus.disconnected);
      }
    }
  }
}
