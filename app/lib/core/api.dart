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

DateTime? _date(Object? v) => v == null ? null : DateTime.parse(v as String).toLocal();

class User {
  User.fromJson(Map<String, dynamic> j)
      : id = j['id'] as int,
        email = j['email'] as String? ?? '',
        rechargeCode = j['recharge_code'] as String? ?? '',
        hasAccount = j['has_account'] as bool? ?? false,
        linked = j['linked'] as bool? ?? false,
        ownerCode = j['owner_code'] as String? ?? '',
        entitlement = Entitlement.fromJson(j['entitlement'] as Map<String, dynamic>);
  final int id;
  final String email; // "" for device-only accounts
  final String rechargeCode; // short top-up id, e.g. "VP-00001"
  final bool hasAccount; // email bound (cross-device) vs device-only
  final bool linked; // uses another account's subscription (multi-device member)
  final String ownerCode; // that account's recharge ID, "" if not linked
  final Entitlement entitlement;

  /// A short display name when there is no email (device-only account).
  String get displayName => hasAccount && email.isNotEmpty ? email : rechargeCode;
}

/// What the user can do right now: plan, traffic quota and speed limit.
class Entitlement {
  Entitlement.fromJson(Map<String, dynamic> j)
      : planId = j['plan_id'] as String? ?? '',
        planName = j['plan_name'] as String? ?? '',
        paid = j['paid'] as bool? ?? false,
        expiresAt = _date(j['expires_at']),
        quotaBytes = (j['quota_bytes'] as num?)?.toInt() ?? 0,
        usedBytes = (j['used_bytes'] as num?)?.toInt() ?? 0,
        periodEnd = _date(j['period_end']),
        speedKbps = (j['speed_kbps'] as num?)?.toInt() ?? 0,
        throttled = j['throttled'] as bool? ?? false,
        canConnect = j['can_connect'] as bool? ?? false,
        reason = j['reason'] as String?;
  final String planId;
  final String planName;
  final bool paid;
  final DateTime? expiresAt;
  final int quotaBytes; // 0 = unlimited
  final int usedBytes;
  final DateTime? periodEnd;
  final int speedKbps; // 0 = unlimited
  final bool throttled;
  final bool canConnect;
  final String? reason;

  double get usedFraction => quotaBytes <= 0 ? 0 : (usedBytes / quotaBytes).clamp(0, 1).toDouble();
}

class Plan {
  Plan.fromJson(Map<String, dynamic> j)
      : id = j['id'] as String,
        name = j['name'] as String,
        description = j['description'] as String? ?? '',
        priceCents = (j['price_cents'] as num).toInt(),
        durationDays = (j['duration_days'] as num).toInt(),
        trafficGb = (j['traffic_gb'] as num).toDouble(),
        speedMbps = (j['speed_mbps'] as num).toInt(),
        overQuotaKbps = (j['over_quota_kbps'] as num).toInt(),
        free = j['free'] as bool? ?? false;
  final String id;
  final String name;
  final String description;
  final int priceCents;
  final int durationDays;
  final double trafficGb;
  final int speedMbps;
  final int overQuotaKbps;
  final bool free;
}

class PayMethod {
  PayMethod.fromJson(Map<String, dynamic> j)
      : id = j['id'] as String,
        name = j['name'] as String;
  final String id;
  final String name;
}

class Order {
  Order.fromJson(Map<String, dynamic> j)
      : id = j['id'] as String,
        planId = j['plan_id'] as String,
        amountCents = (j['amount_cents'] as num).toInt(),
        status = j['status'] as String,
        createdAt = _date(j['created_at'])!,
        paidAt = _date(j['paid_at']);
  final String id;
  final String planId;
  final int amountCents;
  final String status;
  final DateTime createdAt;
  final DateTime? paidAt;
  bool get paid => status == 'paid';
}

class DailyUsage {
  DailyUsage.fromJson(Map<String, dynamic> j)
      : day = j['day'] as String,
        up = (j['up'] as num).toInt(),
        down = (j['down'] as num).toInt();
  final String day;
  final int up;
  final int down;
  int get total => up + down;
}

class Node {
  Node.fromJson(Map<String, dynamic> j)
      : id = j['id'] as String,
        name = j['name'] as String,
        region = j['region'] as String,
        online = j['online'] as bool? ?? true,
        latencyMs = (j['latency_ms'] as num?)?.toInt() ?? 0,
        host = j['host'] as String? ?? '',
        port = (j['port'] as num?)?.toInt() ?? 443,
        uptime = (j['uptime'] as num?)?.toDouble() ?? 1,
        load = (j['load'] as num?)?.toInt() ?? 0;
  final String id;
  final String name;
  final String region;
  final bool online;
  final int latencyMs; // measured by the server; the app prefers its own measurement
  final String host; // for measuring latency from the user's network
  final int port;
  final double uptime; // 0..1 over the last hour
  final int load; // users currently moving traffic
}

/// One device on a shared subscription.
class SharedDevice {
  SharedDevice.fromJson(Map<String, dynamic> j)
      : id = (j['id'] as num).toInt(),
        rechargeCode = j['recharge_code'] as String? ?? '',
        platform = j['platform'] as String? ?? '',
        createdAt = _date(j['created_at']),
        self = j['self'] as bool? ?? false,
        owner = j['owner'] as bool? ?? false;
  final int id;
  final String rechargeCode;
  final String platform;
  final DateTime? createdAt;
  final bool self;
  final bool owner;
}

/// Multi-device sharing state for the current device.
class DeviceShare {
  DeviceShare.fromJson(Map<String, dynamic> j)
      : role = j['role'] as String? ?? 'owner',
        eligible = j['eligible'] as bool? ?? false,
        reason = j['reason'] as String? ?? '',
        maxDevices = (j['max_devices'] as num?)?.toInt() ?? 5,
        ownerCode = j['owner_code'] as String? ?? '',
        devices = [for (final d in (j['devices'] as List? ?? [])) SharedDevice.fromJson(d as Map<String, dynamic>)],
        code = j['code'] as String?,
        codeDisplay = j['code_display'] as String?,
        codeExpiresAt = _date(j['code_expires_at']),
        link = j['link'] as String?;
  final String role; // owner | member
  final bool eligible; // the subscription may be shared
  final String reason; // why not, if !eligible
  final int maxDevices; // total, owner included
  final String ownerCode;
  final List<SharedDevice> devices; // owner first
  final String? code; // active link code (owner only)
  final String? codeDisplay; // "ABCD-EFGH"
  final DateTime? codeExpiresAt;
  final String? link; // https://…/l/CODE, encoded in the QR

  bool get isMember => role == 'member';
}

/// A freshly created link code.
class LinkCode {
  LinkCode.fromJson(Map<String, dynamic> j)
      : code = j['code'] as String,
        display = j['code_display'] as String? ?? j['code'] as String,
        expiresAt = _date(j['expires_at'])!,
        link = j['link'] as String? ?? '';
  final String code;
  final String display;
  final DateTime expiresAt;
  final String link;
}

/// A customer-service or social channel configured in the admin panel.
class Contact {
  Contact.fromJson(Map<String, dynamic> j)
      : id = j['id'] as String,
        type = j['type'] as String,
        group = j['group'] as String? ?? 'service',
        name = j['name'] as String? ?? '',
        value = j['value'] as String? ?? '',
        link = j['link'] as String? ?? '',
        desc = j['desc'] as String? ?? '';
  final String id;
  final String type; // telegram | x | instagram | …
  final String group; // service | social
  final String name;
  final String value; // handle / number / address
  final String link; // "" = copy [value] instead of opening a URL
  final String desc;
}

/// Referral status: the user's own code, a shareable link and reward rules.
class Referral {
  Referral.fromJson(Map<String, dynamic> j)
      : enabled = j['enabled'] as bool? ?? false,
        code = j['code'] as String? ?? '',
        inviteUrl = j['invite_url'] as String? ?? '',
        downloadUrl = j['download_url'] as String? ?? '',
        shareText = j['share_text'] as String? ?? '',
        slogan = j['slogan'] as String? ?? '',
        invitedCount = (j['invited_count'] as num?)?.toInt() ?? 0,
        inviterDays = (j['inviter_days'] as num?)?.toInt() ?? 0,
        inviteeDays = (j['invitee_days'] as num?)?.toInt() ?? 0,
        maxInvites = (j['max_invites'] as num?)?.toInt() ?? 0,
        invited = j['invited'] as bool? ?? false,
        canBind = j['can_bind'] as bool? ?? false;
  final bool enabled;
  final String code;
  final String inviteUrl; // landing page carrying this user's code (what the QR encodes)
  final String downloadUrl;
  final String shareText; // ready-to-paste invite message
  final String slogan;
  final int invitedCount;
  final int inviterDays;
  final int inviteeDays;
  final int maxInvites; // 0 = unlimited
  final bool invited; // this user already entered someone's code
  final bool canBind; // may still enter an inviter's code
}

/// AppRec is a backend-configured recommended app.
class AppRec {
  AppRec.fromJson(Map<String, dynamic> j)
      : id = j['id'] as String,
        name = j['name'] as String,
        iconUrl = j['icon_url'] as String? ?? '',
        url = j['url'] as String,
        category = j['category'] as String? ?? '',
        desc = j['desc'] as String? ?? '';
  final String id;
  final String name;
  final String iconUrl;
  final String url;
  final String category;
  final String desc;
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

  /// No-login authentication: exchanges a stable device id for a session token,
  /// provisioning an account on first contact.
  Future<AuthResult> device(String deviceId, String platform, {String? inviteCode}) async {
    final body = {'device_id': deviceId, 'platform': platform};
    if (inviteCode != null && inviteCode.trim().isNotEmpty) {
      body['invite_code'] = inviteCode.trim();
    }
    final j = await _send('POST', '/api/v1/auth/device', body: body);
    return AuthResult(j['token'] as String, User.fromJson(j['user'] as Map<String, dynamic>));
  }

  Future<AuthResult> login(String email, String password) =>
      _auth('/api/v1/auth/login', email, password);

  Future<AuthResult> register(String email, String password, {String? inviteCode}) =>
      _auth('/api/v1/auth/register', email, password, inviteCode: inviteCode);

  Future<AuthResult> _auth(String path, String email, String password, {String? inviteCode}) async {
    final body = {'email': email, 'password': password};
    if (inviteCode != null && inviteCode.trim().isNotEmpty) {
      body['invite_code'] = inviteCode.trim();
    }
    final j = await _send('POST', path, body: body);
    return AuthResult(j['token'] as String, User.fromJson(j['user'] as Map<String, dynamic>));
  }

  Future<Referral> referral() async => Referral.fromJson(await _send('GET', '/api/v1/referral'));

  /// Enters an inviter's code (new accounts only). Returns the days granted.
  Future<int> bindReferral(String code) async {
    final j = await _send('POST', '/api/v1/referral/bind', body: {'code': code.trim().toUpperCase()});
    return (j['invitee_days'] as num?)?.toInt() ?? 0;
  }

  // ---- multi-device ----
  Future<DeviceShare> devices() async => DeviceShare.fromJson(await _send('GET', '/api/v1/devices'));
  Future<LinkCode> createLinkCode() async => LinkCode.fromJson(await _send('POST', '/api/v1/devices/code'));
  Future<User> joinDevice(String code) async =>
      User.fromJson((await _send('POST', '/api/v1/devices/join', body: {'code': code}))['user'] as Map<String, dynamic>);
  Future<User> leaveDevice() async =>
      User.fromJson((await _send('POST', '/api/v1/devices/leave'))['user'] as Map<String, dynamic>);
  Future<void> removeDevice(int id) => _send('DELETE', '/api/v1/devices/$id');

  Future<List<Contact>> contacts() async {
    final j = await _send('GET', '/api/v1/contacts');
    return (j['contacts'] as List).map((e) => Contact.fromJson(e as Map<String, dynamic>)).toList();
  }

  Future<List<AppRec>> apps() async {
    final j = await _send('GET', '/api/v1/apps');
    return (j['apps'] as List).map((e) => AppRec.fromJson(e as Map<String, dynamic>)).toList();
  }

  Future<User> me() async => User.fromJson(await _send('GET', '/api/v1/me'));

  Future<List<Node>> nodes() async {
    final j = await _send('GET', '/api/v1/nodes');
    return (j['nodes'] as List).map((e) => Node.fromJson(e as Map<String, dynamic>)).toList();
  }

  Future<(List<Plan>, List<PayMethod>)> plans() async {
    final j = await _send('GET', '/api/v1/plans');
    return (
      (j['plans'] as List).map((e) => Plan.fromJson(e as Map<String, dynamic>)).toList(),
      (j['methods'] as List).map((e) => PayMethod.fromJson(e as Map<String, dynamic>)).toList(),
    );
  }

  /// Creates an order and returns it with the URL the user opens to pay.
  Future<(Order, Uri)> createOrder(String planId, String method) async {
    final j = await _send('POST', '/api/v1/orders', body: {'plan_id': planId, 'method': method});
    return (Order.fromJson(j['order'] as Map<String, dynamic>), Uri.parse(j['pay_url'] as String));
  }

  Future<Order> order(String id) async => Order.fromJson(await _send('GET', '/api/v1/orders/$id'));

  Future<List<Order>> orders() async {
    final j = await _send('GET', '/api/v1/orders');
    return (j['orders'] as List).map((e) => Order.fromJson(e as Map<String, dynamic>)).toList();
  }

  Future<List<DailyUsage>> usage() async {
    final j = await _send('GET', '/api/v1/usage');
    return (j['days'] as List).map((e) => DailyUsage.fromJson(e as Map<String, dynamic>)).toList();
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
