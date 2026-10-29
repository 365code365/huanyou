import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:http/http.dart' as http;

import 'config.dart';

class ApiException implements Exception {
  ApiException(this.status, this.code, this.message);
  final int status;
  final String code;
  final String message;

  bool get unauthorized => status == 401;

  @override
  String toString() => message;
}

class User {
  User.fromJson(Map<String, dynamic> j)
      : id = j['id'] as int,
        email = j['email'] as String,
        active = j['active'] as bool,
        expiresAt = j['expires_at'] == null ? null : DateTime.parse(j['expires_at'] as String);
  final int id;
  final String email;
  final bool active;
  final DateTime? expiresAt;
}

class Node {
  Node.fromJson(Map<String, dynamic> j)
      : id = j['id'] as String,
        name = j['name'] as String,
        region = j['region'] as String;
  final String id;
  final String name;
  final String region;
}

class Profile {
  Profile({required this.node, required this.mode, required this.config});
  Profile.fromJson(Map<String, dynamic> j)
      : node = Node.fromJson(j['node'] as Map<String, dynamic>),
        mode = j['mode'] as String,
        config = j['config'] as String;
  final Node node;
  final String mode; // "smart" | "global"
  final String config; // Xray client JSON, passed to the native core as-is

  Map<String, dynamic> toJson() => {
        'node': {'id': node.id, 'name': node.name, 'region': node.region},
        'mode': mode,
        'config': config,
      };
}

class AuthResult {
  AuthResult(this.token, this.user);
  final String token;
  final User user;
}

class ApiClient {
  ApiClient({List<Uri>? baseUrls}) : _bases = baseUrls ?? AppConfig.apiBaseUrls;

  final List<Uri> _bases;
  final http.Client _http = http.Client();
  int _preferred = 0; // index of the last base URL that worked
  String? token;

  Future<AuthResult> login(String email, String password) =>
      _auth('/api/v1/auth/login', email, password);

  Future<AuthResult> register(String email, String password) =>
      _auth('/api/v1/auth/register', email, password);

  Future<AuthResult> _auth(String path, String email, String password) async {
    final j = await _send('POST', path, body: {'email': email, 'password': password});
    return AuthResult(j['token'] as String, User.fromJson(j['user'] as Map<String, dynamic>));
  }

  Future<User> me() async => User.fromJson(await _send('GET', '/api/v1/me'));

  Future<List<Node>> nodes() async {
    final j = await _send('GET', '/api/v1/nodes');
    return (j['nodes'] as List).map((e) => Node.fromJson(e as Map<String, dynamic>)).toList();
  }

  Future<Profile> profile({String? nodeId, String mode = 'smart'}) async {
    final q = {'mode': mode, 'node': ?nodeId};
    return Profile.fromJson(await _send('GET', '/api/v1/profile', query: q));
  }

  /// Tries each base URL, starting from the last one that worked.
  /// Only network-level failures move on to the next URL; HTTP errors are returned.
  Future<Map<String, dynamic>> _send(String method, String path,
      {Map<String, dynamic>? body, Map<String, String>? query}) async {
    Object? lastError;
    for (var i = 0; i < _bases.length; i++) {
      final idx = (_preferred + i) % _bases.length;
      final uri = _bases[idx].replace(path: path, queryParameters: query);
      try {
        final req = http.Request(method, uri)
          ..headers['Content-Type'] = 'application/json'
          ..headers['Accept'] = 'application/json';
        if (token != null) req.headers['Authorization'] = 'Bearer $token';
        if (body != null) req.body = jsonEncode(body);
        final resp = await http.Response.fromStream(
            await _http.send(req).timeout(const Duration(seconds: 12)));
        _preferred = idx;
        final decoded = resp.body.isEmpty ? <String, dynamic>{} : jsonDecode(utf8.decode(resp.bodyBytes));
        if (resp.statusCode >= 200 && resp.statusCode < 300) {
          return decoded as Map<String, dynamic>;
        }
        final m = decoded is Map<String, dynamic> ? decoded : <String, dynamic>{};
        throw ApiException(resp.statusCode, m['error'] as String? ?? 'http_${resp.statusCode}',
            m['message'] as String? ?? '请求失败 (${resp.statusCode})');
      } on ApiException {
        rethrow;
      } on SocketException catch (e) {
        lastError = e;
      } on TimeoutException catch (e) {
        lastError = e;
      } on http.ClientException catch (e) {
        lastError = e;
      } on HandshakeException catch (e) {
        lastError = e;
      }
    }
    throw ApiException(0, 'network', '无法连接服务器，请检查网络 ($lastError)');
  }
}
