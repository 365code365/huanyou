import 'dart:convert';
import 'dart:math';

import 'api.dart';
import 'device.dart';
import 'prefs.dart';

/// Persists the login token, chosen node/mode, the last profile (so the app
/// can reconnect even when the API is temporarily unreachable) and the last
/// account / node list (shown instantly at the next start). Values live in
/// secure storage and are read through the in-memory [Prefs] mirror.
class Session {
  static const _kToken = 'token';
  static const _kNode = 'node_id';
  static const _kMode = 'mode';
  static const _kProfile = 'profile';
  static const _kDevice = 'device_id';
  static const _kMe = 'snapshot_me';
  static const _kNodes = 'snapshot_nodes';

  Future<String?> token() async => Prefs.get(_kToken);
  Future<void> saveToken(String t) => Prefs.set(_kToken, t);

  /// A stable id for this device used for no-login auth. On Android it is
  /// derived from ANDROID_ID (survives reinstall); on iOS a random id is
  /// generated once and kept in the Keychain (also survives reinstall).
  /// Once resolved it is cached in secure storage so it never changes.
  Future<String> deviceId() async {
    final existing = Prefs.get(_kDevice);
    if (existing != null && existing.length >= 8) return existing;
    String id;
    final native = (await Device.nativeId())?.trim() ?? '';
    if (native.length >= 8 && native != '9774d56d682e549c') {
      // Prefix namespaces the source and keeps the id opaque to the server.
      id = 'and-$native';
    } else {
      id = 'kc-${_randomHex(20)}';
    }
    await Prefs.set(_kDevice, id);
    return id;
  }

  static String _randomHex(int bytes) {
    final r = Random.secure();
    final b = List<int>.generate(bytes, (_) => r.nextInt(256));
    return b.map((x) => x.toRadixString(16).padLeft(2, '0')).join();
  }

  Future<String?> nodeId() async => Prefs.get(_kNode);
  Future<void> saveNodeId(String id) => Prefs.set(_kNode, id);

  /// Connection mode: direct | cluster | ai. Defaults to direct (highest priority).
  Future<String> mode() async {
    final m = Prefs.get(_kMode);
    const valid = {'direct', 'cluster', 'ai'};
    return valid.contains(m) ? m! : 'direct';
  }

  Future<void> saveMode(String m) => Prefs.set(_kMode, m);

  Future<Profile?> cachedProfile() async {
    final s = Prefs.get(_kProfile);
    if (s == null) return null;
    try {
      final p = Profile.fromJson(jsonDecode(s) as Map<String, dynamic>);
      // Profiles cached by older versions are JSON; the slim core needs compiled ones.
      if (p.config.trimLeft().startsWith('{')) return null;
      return p;
    } catch (_) {
      return null;
    }
  }

  Future<void> saveProfile(Profile p) => Prefs.set(_kProfile, jsonEncode(p.toJson()));

  /// Last /me and /nodes responses (raw JSON), or null.
  (Map<String, dynamic>, List<dynamic>)? snapshot() {
    final me = Prefs.get(_kMe), nodes = Prefs.get(_kNodes);
    if (me == null || nodes == null) return null;
    try {
      return (jsonDecode(me) as Map<String, dynamic>, jsonDecode(nodes) as List<dynamic>);
    } catch (_) {
      return null;
    }
  }

  /// Saves the snapshot only when it changed (each write re-encrypts the value).
  void saveSnapshot(Map<String, dynamic> me, List<dynamic> nodes) {
    final a = jsonEncode(me), b = jsonEncode(nodes);
    if (Prefs.get(_kMe) != a) Prefs.set(_kMe, a);
    if (Prefs.get(_kNodes) != b) Prefs.set(_kNodes, b);
  }

  /// Clears the session but keeps the device id, so the user returns to the
  /// same device account (and reinstall recognition still works).
  Future<void> clear() async {
    await Future.wait([
      for (final k in [_kToken, _kNode, _kMode, _kProfile, _kMe, _kNodes]) Prefs.remove(k),
    ]);
  }
}
