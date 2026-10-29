import 'dart:convert';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';

import 'api.dart';

/// Persists the login token, chosen node/mode and the last profile
/// (so the app can reconnect even when the API is temporarily unreachable).
class Session {
  static const _storage = FlutterSecureStorage();
  static const _kToken = 'token';
  static const _kNode = 'node_id';
  static const _kMode = 'mode';
  static const _kProfile = 'profile';

  Future<String?> token() => _storage.read(key: _kToken);
  Future<void> saveToken(String t) => _storage.write(key: _kToken, value: t);

  Future<String?> nodeId() => _storage.read(key: _kNode);
  Future<void> saveNodeId(String id) => _storage.write(key: _kNode, value: id);

  Future<String> mode() async => await _storage.read(key: _kMode) ?? 'smart';
  Future<void> saveMode(String m) => _storage.write(key: _kMode, value: m);

  Future<Profile?> cachedProfile() async {
    final s = await _storage.read(key: _kProfile);
    if (s == null) return null;
    try {
      return Profile.fromJson(jsonDecode(s) as Map<String, dynamic>);
    } catch (_) {
      return null;
    }
  }

  Future<void> saveProfile(Profile p) => _storage.write(key: _kProfile, value: jsonEncode(p.toJson()));

  Future<void> clear() => _storage.deleteAll();
}
