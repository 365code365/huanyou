import 'dart:convert';
import 'dart:math';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';

import 'api.dart';
import 'device.dart';

/// Persists the login token, chosen node/mode and the last profile
/// (so the app can reconnect even when the API is temporarily unreachable).
class Session {
  static const _storage = FlutterSecureStorage();
  static const _kToken = 'token';
  static const _kNode = 'node_id';
  static const _kMode = 'mode';
  static const _kProfile = 'profile';
  static const _kDevice = 'device_id';

  Future<String?> token() => _storage.read(key: _kToken);
  Future<void> saveToken(String t) => _storage.write(key: _kToken, value: t);

  /// A stable id for this device used for no-login auth. On Android it is
  /// derived from ANDROID_ID (survives reinstall); on iOS a random id is
  /// generated once and kept in the Keychain (also survives reinstall).
  /// Once resolved it is cached in secure storage so it never changes.
  Future<String> deviceId() async {
    final existing = await _storage.read(key: _kDevice);
    if (existing != null && existing.length >= 8) return existing;
    String id;
    final native = (await Device.nativeId())?.trim() ?? '';
    if (native.length >= 8 && native != '9774d56d682e549c') {
      // Prefix namespaces the source and keeps the id opaque to the server.
      id = 'and-$native';
    } else {
      id = 'kc-${_randomHex(20)}';
    }
    await _storage.write(key: _kDevice, value: id);
    return id;
  }

  static String _randomHex(int bytes) {
    final r = Random.secure();
    final b = List<int>.generate(bytes, (_) => r.nextInt(256));
    return b.map((x) => x.toRadixString(16).padLeft(2, '0')).join();
  }

  Future<String?> nodeId() => _storage.read(key: _kNode);
  Future<void> saveNodeId(String id) => _storage.write(key: _kNode, value: id);

  /// Connection mode: direct | cluster | ai. Defaults to direct (highest priority).
  Future<String> mode() async {
    final m = await _storage.read(key: _kMode);
    const valid = {'direct', 'cluster', 'ai'};
    return valid.contains(m) ? m! : 'direct';
  }

  Future<void> saveMode(String m) => _storage.write(key: _kMode, value: m);

  Future<Profile?> cachedProfile() async {
    final s = await _storage.read(key: _kProfile);
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

  Future<void> saveProfile(Profile p) => _storage.write(key: _kProfile, value: jsonEncode(p.toJson()));

  /// Clears the session but keeps the device id, so the user returns to the
  /// same device account (and reinstall recognition still works).
  Future<void> clear() async {
    await Future.wait([
      _storage.delete(key: _kToken),
      _storage.delete(key: _kNode),
      _storage.delete(key: _kMode),
      _storage.delete(key: _kProfile),
    ]);
  }
}
