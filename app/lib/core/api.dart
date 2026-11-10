import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:http/http.dart' as http;
import 'package:http/io_client.dart';

import '../i18n/i18n.dart';
import 'config.dart';
import 'integrity.dart';
import 'secure.dart';
import 'prefs.dart';

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
        free = j['free'] as bool? ?? false,
        hot = j['hot'] as bool? ?? false,
        iapProductId = j['iap_product_id'] as String? ?? '',
        gpProductId = j['gp_product_id'] as String? ?? '';
  final String id;
  final String name;
  final String description;
  final int priceCents;
  final int durationDays;
  final double trafficGb;
  final int speedMbps;
  final int overQuotaKbps;
  final bool free;
  final bool hot; // 🔥 HOT badge
  final String iapProductId; // App Store product id, only set on the iOS IAP face
  final String gpProductId; // Google Play product id, only set on the Play face
}

/// Whether purchases are open (admin switch); notice is shown when not.
/// [mode] is how to pay: "gateway" (web / custom), "iap" (Apple in-app
/// purchase, shown to App Review on iOS) or "gplay" (Google Play Billing, the
/// Google Play Android build). "gateway" by default.
class PurchaseState {
  const PurchaseState(
      {required this.enabled, required this.notice, this.mode = 'gateway', this.termsUrl = '', this.privacyUrl = ''});
  final bool enabled;
  final String notice;
  final String mode;
  final String termsUrl; // 用户协议, linked from the purchase page
  final String privacyUrl; // 隐私政策
  bool get iap => mode == 'iap'; // Apple App Store
  bool get gplay => mode == 'gplay'; // Google Play
  /// An official app-store face: buy through the store, not a web page.
  bool get store => iap || gplay;
}

/// Result of "恢复购买".
class RestoreResult {
  RestoreResult.fromJson(Map<String, dynamic> j)
      : granted = (j['granted'] as num?)?.toInt() ?? 0,
        linkedTo = j['linked_to'] as String? ?? '';
  const RestoreResult.empty()
      : granted = 0,
        linkedTo = '';
  final int granted; // purchases newly granted to this device
  final String linkedTo; // recharge id of the account this device joined ("" = none)
}

/// One home banner / launch popup from GET /api/v1/ads.
class AdItem {
  AdItem.fromJson(Map<String, dynamic> j)
      : id = j['id'] as String? ?? '',
        title = j['title'] as String? ?? '',
        text = j['text'] as String? ?? '',
        image = j['image'] as String? ?? '',
        link = j['link'] as String? ?? '',
        open = j['open'] as String? ?? 'browser';
  final String id, title, text, image, link;
  final String open; // browser | webview (in-app page)
}

class Ads {
  Ads.fromJson(Map<String, dynamic> j)
      : interval = ((j['home'] as Map?)?['interval'] as num?)?.toInt() ?? 5,
        home = [
          for (final e in ((j['home'] as Map?)?['items'] as List?) ?? const [])
            AdItem.fromJson(e as Map<String, dynamic>)
        ],
        popupFrequency = ((j['popup'] as Map?)?['frequency'] as String?) ?? 'daily',
        popup = ((j['popup'] as Map?)?['item'] is Map<String, dynamic>)
            ? AdItem.fromJson((j['popup'] as Map)['item'] as Map<String, dynamic>)
            : null,
        marquee = [
          for (final e in ((j['marquee'] as Map?)?['items'] as List?) ?? const [])
            AdItem.fromJson(e as Map<String, dynamic>)
        ];
  final int interval;
  final List<AdItem> home;
  final List<AdItem> marquee; // scrolling announcements on the home page
  final String popupFrequency;
  final AdItem? popup;
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

/// Result of an update check.
class AppUpdate {
  AppUpdate.fromJson(Map<String, dynamic> j)
      : hasUpdate = j['has_update'] as bool? ?? false,
        force = j['force'] as bool? ?? false,
        version = (j['latest'] as Map?)?['version'] as String? ?? '',
        build = ((j['latest'] as Map?)?['build'] as num?)?.toInt() ?? 0,
        notes = (j['latest'] as Map?)?['notes'] as String? ?? '',
        url = (j['latest'] as Map?)?['url'] as String? ?? '',
        size = ((j['latest'] as Map?)?['size'] as num?)?.toInt() ?? 0,
        sha256 = (j['latest'] as Map?)?['sha256'] as String? ?? '',
        kind = _kindOf((j['latest'] as Map?)?['kind'] as String? ?? '', (j['latest'] as Map?)?['url'] as String? ?? '');

  /// The server's package type, or the URL's extension for older servers.
  static String _kindOf(String kind, String url) {
    if (kind.isNotEmpty) return kind;
    final path = Uri.tryParse(url)?.path.toLowerCase() ?? '';
    for (final k in ['exe', 'zip', 'dmg', 'apk', 'ipa']) {
      if (path.endsWith('.$k')) return k;
    }
    return '';
  }
  final bool hasUpdate;
  final bool force; // the installed build is below a forced release
  final String version;
  final int build;
  final String notes;
  final String url; // APK file (Android) or install / store link (iOS)
  final int size;
  final String sha256; // "" for external links
  final String kind; // package type: exe (Windows installer), zip, dmg, apk, ipa, "" unknown
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
/// Days as shown to users: 3 → "3", 0.5 → "0.5".
String fmtDays(double d) => d == d.roundToDouble() ? d.toInt().toString() : d.toStringAsFixed(1);

class Referral {
  Referral.fromJson(Map<String, dynamic> j)
      : enabled = j['enabled'] as bool? ?? false,
        code = j['code'] as String? ?? '',
        inviteUrl = j['invite_url'] as String? ?? '',
        downloadUrl = j['download_url'] as String? ?? '',
        shareText = j['share_text'] as String? ?? '',
        slogan = j['slogan'] as String? ?? '',
        invitedCount = (j['invited_count'] as num?)?.toInt() ?? 0,
        inviterDays = (j['inviter_days'] as num?)?.toDouble() ?? 0,
        inviteeDays = (j['invitee_days'] as num?)?.toDouble() ?? 0,
        inviterPaidDays = (j['inviter_paid_days'] as num?)?.toDouble() ?? 0,
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
  final double inviterDays; // when the friend signs up (may be 0.5)
  final double inviteeDays;
  final double inviterPaidDays; // extra when the friend first pays
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
  AuthResult(this.token, this.user, this.keys);
  final String token;
  final User user;
  final SessionKeys? keys; // null only against a server from before session keys
}

class ApiClient {
  ApiClient({List<Uri>? baseUrls}) : _bases = baseUrls ?? AppConfig.apiBaseUrls;

  final List<Uri> _bases;
  final http.Client _http = IOClient(_ApiConnections.client());
  static const _kPreferred = 'api_preferred';
  // The base URL that worked last, kept across launches so a cold start does
  // not wait for a blocked first URL to time out every time.
  late int _preferred = () {
    final i = _bases.indexWhere((b) => b.toString() == Prefs.get(_kPreferred));
    return i < 0 ? 0 : i;
  }();
  void _prefer(int idx) {
    if (idx == _preferred) return;
    _preferred = idx;
    Prefs.set(_kPreferred, _bases[idx].toString());
  }
  String? token;

  /// The session's keys (secure.dart); with them every request is signed and
  /// responses arrive encrypted.
  SessionKeys? keys;

  // Server clock minus ours (seconds), from X-HY-Time: request signatures
  // carry the time, and a phone's clock can be far off.
  int _clockOffset = 0;

  // The app's platform and build, sent on /plans and /apps so the server can
  // serve the right face (iOS App Review vs. normal). Set once at startup.
  String clientPlatform = '';
  int clientBuild = 0;
  Map<String, String> get _clientTag => clientPlatform.isEmpty ? const {} : {'platform': clientPlatform, 'build': '$clientBuild'};

  /// No-login authentication: exchanges a stable device id for a session token,
  /// provisioning an account on first contact.
  Future<AuthResult> device(String deviceId, String platform, {String? inviteCode}) async {
    final kx = await KeyExchange.start();
    final body = {'device_id': deviceId, 'platform': platform, 'pub': kx.publicB64};
    if (inviteCode != null && inviteCode.trim().isNotEmpty) {
      body['invite_code'] = inviteCode.trim();
    }
    return _authResult(await _send('POST', '/api/v1/auth/device', body: body), kx);
  }

  /// Upgrades a token saved by an app version from before session keys.
  Future<AuthResult> rekey() async {
    final kx = await KeyExchange.start();
    return _authResult(await _send('POST', '/api/v1/auth/rekey', body: {'pub': kx.publicB64}), kx);
  }

  /// A sign-in reply: sealed for this client (token + account), or plain
  /// from a server without session keys.
  Future<AuthResult> _authResult(Map<String, dynamic> j, KeyExchange kx) async {
    final sealed = j['sealed'];
    if (sealed is String && j['server_pub'] is String) {
      final k = await kx.finish(j['server_pub'] as String);
      final plain = await openSealed(k.enc, [...utf8.encode('auth:'), ...kx.publicKey], base64Decode(sealed));
      final m = jsonDecode(utf8.decode(plain)) as Map<String, dynamic>;
      return AuthResult(m['token'] as String, User.fromJson(m['user'] as Map<String, dynamic>), k);
    }
    return AuthResult(j['token'] as String, User.fromJson(j['user'] as Map<String, dynamic>), null);
  }

  Future<AuthResult> login(String email, String password) =>
      _auth('/api/v1/auth/login', email, password);

  Future<AuthResult> register(String email, String password, {String? inviteCode}) =>
      _auth('/api/v1/auth/register', email, password, inviteCode: inviteCode);

  Future<AuthResult> _auth(String path, String email, String password, {String? inviteCode}) async {
    final kx = await KeyExchange.start();
    final body = {'email': email, 'password': password, 'pub': kx.publicB64};
    if (inviteCode != null && inviteCode.trim().isNotEmpty) {
      body['invite_code'] = inviteCode.trim();
    }
    return _authResult(await _send('POST', path, body: body), kx);
  }

  Future<Referral> referral() async => Referral.fromJson(await _send('GET', '/api/v1/referral'));

  /// Enters an inviter's code (new accounts only). Returns the days granted.
  Future<double> bindReferral(String code) async {
    final j = await _send('POST', '/api/v1/referral/bind', body: {'code': code.trim().toUpperCase()});
    return (j['invitee_days'] as num?)?.toDouble() ?? 0;
  }

  Future<AppUpdate> appUpdate(String platform, int build, {String abi = ''}) async => AppUpdate.fromJson(
      // installer=1: this app can install a Windows setup.exe in place (older
      // ones get no checksum for it and download it in the browser instead).
      await _send('GET', '/api/v1/app/update',
          query: {'platform': platform, 'build': '$build', if (abi.isNotEmpty) 'abi': abi, 'installer': '1'}));

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
    final j = await _send('GET', '/api/v1/apps', query: _clientTag);
    return (j['apps'] as List).map((e) => AppRec.fromJson(e as Map<String, dynamic>)).toList();
  }

  Future<User> me() async => User.fromJson(await _send('GET', '/api/v1/me'));

  /// Raw GET for callers that cache the response body.
  Future<Map<String, dynamic>> getJson(String path) => _send('GET', path);

  Future<List<Node>> nodes() async {
    final j = await _send('GET', '/api/v1/nodes');
    return (j['nodes'] as List).map((e) => Node.fromJson(e as Map<String, dynamic>)).toList();
  }

  Future<(List<Plan>, List<PayMethod>)> plans() async {
    final r = await plansFull();
    return (r.$1, r.$2);
  }

  /// Plans, payment methods and whether purchases are open.
  Future<(List<Plan>, List<PayMethod>, PurchaseState)> plansFull() async {
    final j = await _send('GET', '/api/v1/plans', query: _clientTag);
    final p = j['purchase'] as Map<String, dynamic>?;
    final legal = j['legal'] as Map<String, dynamic>?;
    return (
      (j['plans'] as List).map((e) => Plan.fromJson(e as Map<String, dynamic>)).toList(),
      (j['methods'] as List).map((e) => PayMethod.fromJson(e as Map<String, dynamic>)).toList(),
      PurchaseState(
          enabled: p?['enabled'] as bool? ?? true,
          notice: p?['notice'] as String? ?? '',
          mode: j['pay_mode'] as String? ?? 'gateway',
          termsUrl: legal?['terms_url'] as String? ?? '',
          privacyUrl: legal?['privacy_url'] as String? ?? ''),
    );
  }

  /// Verifies an App Store purchase receipt (iOS IAP face) and grants the plan.
  Future<void> verifyIap(String planId, String receipt) =>
      _send('POST', '/api/v1/iap/verify', body: {'plan_id': planId, 'receipt': receipt});

  /// "恢复购买": sends the App Store receipt; the server grants unredeemed
  /// purchases or joins this device to the account that bought them.
  Future<RestoreResult> restoreIap(String receipt) async =>
      RestoreResult.fromJson(await _send('POST', '/api/v1/iap/restore', body: {'receipt': receipt}));

  /// Verifies a Google Play purchase and grants the plan. [data] is the Play
  /// Billing purchase JSON (originalJson); [productId] and [token] are the
  /// parsed fields the server falls back to. The server maps the product id
  /// back to a plan and verifies the token with the Play Developer API.
  Future<void> verifyGooglePlay({required String data, required String productId, required String token}) =>
      _send('POST', '/api/v1/iap/google/verify', body: {'data': data, 'product_id': productId, 'token': token});

  Future<Ads> ads() async => Ads.fromJson(await _send('GET', '/api/v1/ads'));

  /// Fire-and-forget POST (analytics / crash reports); errors are swallowed by the caller.
  Future<void> post(String path, Map<String, dynamic> body) => _send('POST', path, body: body);

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

  /// Languages with a downloadable pack (Chinese is built in and not listed).
  Future<List<LangInfo>> languages() async {
    final j = await _send('GET', '/api/v1/i18n');
    return [for (final e in j['languages'] as List) LangInfo.fromJson(e as Map<String, dynamic>)];
  }

  /// {code, version, rtl, strings: {中文原文: 译文}}
  Future<Map<String, dynamic>> languagePack(String code) => _send('GET', '/api/v1/i18n/${Uri.encodeComponent(code)}');

  Future<Profile> profile({String? nodeId, String mode = 'smart'}) async {
    // format=pb: the server compiles the profile (smart-routing IP lists
    // included) so the native core needs no JSON loader or geo files.
    final q = {'mode': mode, 'format': 'pb', 'node': ?nodeId};
    return Profile.fromJson(await _send('GET', '/api/v1/profile', query: q));
  }

  /// Tries each base URL, starting from the last one that worked.
  /// Network-level failures (and responses that are not our JSON, e.g. a
  /// captive portal or a blocking page) move on to the next URL; API errors
  /// are returned as they are.
  ///
  /// Reads (GET) are retried once on the same URL when a kept-alive
  /// connection turns out to be dead. Writes are only re-sent elsewhere when
  /// the connection could not be made at all, so an order or a device link
  /// is never created twice.
  Future<Map<String, dynamic>> _send(String method, String path,
      {Map<String, dynamic>? body, Map<String, String>? query}) async {
    try {
      return await _sendOnce(method, path, body: body, query: query);
    } on _ClockSkew {
      // The clock offset was just corrected from the server's reply: once more.
      try {
        return await _sendOnce(method, path, body: body, query: query);
      } on _ClockSkew {
        // Not 401: this is no reason to sign in again.
        throw ApiException(400, 'bad_time', tr('设备时间不正确，请在系统设置中开启自动设置时间'));
      }
    }
  }

  /// Signs [req] (secure.dart); returns the nonce, which also binds the
  /// encrypted reply to this request.
  String _sign(http.Request req, SessionKeys k, List<int> body) {
    final ts = '${DateTime.now().millisecondsSinceEpoch ~/ 1000 + _clockOffset}';
    final nonce = newNonce();
    final full = req.url.path;
    final i = full.indexOf('/api/');
    final path = i > 0 ? full.substring(i) : full;
    final env = Integrity.env;
    req.headers['X-HY-TS'] = ts;
    req.headers['X-HY-Nonce'] = nonce;
    if (env.isNotEmpty) req.headers['X-HY-Env'] = env;
    req.headers['X-HY-Sig'] = signRequest(k, canonicalRequest(req.method, path, req.url.query, ts, nonce, env, body));
    return nonce;
  }

  Future<Map<String, dynamic>> _sendOnce(String method, String path,
      {Map<String, dynamic>? body, Map<String, String>? query}) async {
    Object? lastError;
    final payload = body == null ? const <int>[] : utf8.encode(jsonEncode(body));
    final read = method == 'GET';
    final tries = _bases.length < 2 && read ? 2 : _bases.length;
    for (var i = 0; i < tries; i++) {
      if (i > 0 && !read && !_neverSent(lastError)) break;
      if (i > 0 && _bases.length < 2 && lastError is TimeoutException) break;
      final idx = (_preferred + i) % _bases.length;
      final base = _bases[idx];
      // Keep a path prefix on the base URL (e.g. https://x.com/vpn).
      final prefix = base.path.endsWith('/') ? base.path.substring(0, base.path.length - 1) : base.path;
      final uri = base.replace(path: prefix + path, queryParameters: query);
      try {
        final req = http.Request(method, uri)
          ..headers['Content-Type'] = 'application/json'
          ..headers['Accept'] = 'application/json';
        if (token != null) req.headers['Authorization'] = 'Bearer $token';
        if (body != null) req.bodyBytes = payload;
        // Fresh nonce per attempt: a retry is a new request.
        final k = token != null ? keys : null;
        final nonce = k == null ? null : _sign(req, k, payload);
        final resp = await _http
            .send(req)
            .timeout(const Duration(seconds: 12))
            .then((r) => http.Response.fromStream(r).timeout(const Duration(seconds: 20)));
        final serverTime = int.tryParse(resp.headers['x-hy-time'] ?? '');
        if (serverTime != null) _clockOffset = serverTime - DateTime.now().millisecondsSinceEpoch ~/ 1000;
        List<int> bytes = resp.bodyBytes;
        final enc = resp.headers['x-hy-enc'];
        if (enc != null) {
          // Sealed reply: only for a signed request, bound to its nonce. Any
          // change on the way (or a stranger answering) fails to open.
          if (k == null || nonce == null) throw _NotOurs(resp.statusCode);
          try {
            bytes = await openSealed(k.enc, utf8.encode('resp:$nonce'), bytes);
            if (enc.contains('z')) bytes = gzip.decode(bytes);
          } catch (_) {
            throw _NotOurs(resp.statusCode);
          }
        }
        final Object? decoded;
        try {
          decoded = bytes.isEmpty ? <String, dynamic>{} : jsonDecode(utf8.decode(bytes));
        } on FormatException {
          // Not our API (portal login page, hijacked DNS, CDN block page).
          throw _NotOurs(resp.statusCode);
        }
        if (resp.statusCode >= 200 && resp.statusCode < 300) {
          if (decoded is! Map<String, dynamic>) throw _NotOurs(resp.statusCode);
          _prefer(idx);
          return decoded;
        }
        final m = decoded is Map<String, dynamic> ? decoded : <String, dynamic>{};
        if (m['error'] is! String && resp.statusCode >= 500) throw _NotOurs(resp.statusCode); // proxy error page
        _prefer(idx);
        if (m['error'] == 'bad_time' && serverTime != null) throw const _ClockSkew();
        // Server messages are Chinese; packs may carry translations for them.
        final msg = m['message'] as String?;
        throw ApiException(resp.statusCode, m['error'] as String? ?? 'http_${resp.statusCode}',
            msg != null ? tr(msg) : tr('请求失败 ({0})', [resp.statusCode]));
      } on ApiException {
        rethrow;
      } on _NotOurs catch (e) {
        lastError = e;
      } on _ConnectFailed catch (e) {
        lastError = e;
      } on SocketException catch (e) {
        lastError = e;
      } on TimeoutException catch (e) {
        lastError = e;
      } on http.ClientException catch (e) {
        lastError = e;
      } on HandshakeException catch (e) {
        lastError = e;
      } on TlsException catch (e) {
        lastError = e;
      }
    }
    throw ApiException(0, 'network', _networkMessage(lastError));
  }

  /// True when the request certainly never reached our server: no
  /// connection could be made (DNS, refused, timeout, TLS handshake), or what
  /// answered was not our API (portal / block page). Safe to send elsewhere.
  static bool _neverSent(Object? e) =>
      e is _ConnectFailed || e is _NotOurs || e is HandshakeException || e is TlsException;

  static String _networkMessage(Object? error) {
    final e = error is _ConnectFailed ? error.cause : error;
    if (e is TimeoutException) return tr('连接服务器超时，请检查网络后重试');
    if (e is HandshakeException || e is TlsException || e is _NotOurs) {
      return tr('网络环境异常（可能需要先登录 Wi-Fi 认证页），请切换网络后重试');
    }
    return tr('无法连接服务器，请检查网络后重试');
  }
}

/// The server refused the request time; the clock offset has been corrected.
class _ClockSkew implements Exception {
  const _ClockSkew();
}

/// No connection to the API host could be made, so no request was sent.
class _ConnectFailed implements Exception {
  _ConnectFailed(this.cause);
  final Object cause;
  @override
  String toString() => 'connect failed: $cause';
}

/// A response that is not from our API.
class _NotOurs implements Exception {
  _NotOurs(this.status);
  final int status;
  @override
  String toString() => 'unexpected response (HTTP $status)';
}

/// Connections to the API.
///
/// While a VPN is up, the system resolver can be slow or time out for the
/// app (Android routes the lookup around the tunnel, some ROMs stall on it),
/// and every new connection paid for that. So the API host's address is
/// cached (memory + disk) and refreshed in the background, connections are
/// made to the cached IP (TLS still verifies the host name), and idle
/// connections are kept longer so most requests reuse one.
class _ApiConnections {
  static const _prefsKey = 'api_dns';
  static const _fresh = Duration(minutes: 10);
  static final Map<String, (List<InternetAddress>, DateTime)> _cache = {};
  static final Map<String, Future<List<InternetAddress>>> _inflight = {};

  static HttpClient client() {
    final c = HttpClient()
      ..idleTimeout = const Duration(seconds: 90)
      // Backstop only: open() below gives up after 10 s with _ConnectFailed,
      // which (unlike HttpClient's own timeout) marks the request as unsent.
      ..connectionTimeout = const Duration(seconds: 15);
    c.connectionFactory = (Uri uri, String? proxyHost, int? proxyPort) async {
      final port = uri.hasPort ? uri.port : (uri.scheme == 'https' ? 443 : 80);
      var cancelled = false;
      Socket? pending; // the socket being connected / handshaken right now
      Future<Socket> open() async {
        Object? last;
        final tried = <String>{};
        // Cached addresses first; if none answers (the server moved), a
        // fresh lookup and once more.
        for (final fresh in [false, true]) {
          if (cancelled) break;
          final List<InternetAddress> addrs;
          try {
            addrs = fresh ? await _lookup(uri.host) : await _resolve(uri.host);
          } catch (e) {
            last = e;
            continue;
          }
          for (final a in addrs.take(3)) {
            if (cancelled) break;
            if (!tried.add(a.address)) continue;
            Socket? s;
            try {
              s = pending = await Socket.connect(a, port, timeout: const Duration(seconds: 6));
              if (cancelled) {
                s.destroy();
                break;
              }
              if (uri.scheme != 'https') return s;
              // A blocked SNI can accept TCP and then never answer the TLS
              // handshake: bound it, or the attempt hangs forever.
              // Release builds only accept the pinned CA roots (secure.dart).
              final pin = pinningEnabled && !isLocalHost(uri.host);
              final tls = await SecureSocket.secure(s, host: uri.host, context: pin ? pinnedContext() : null)
                  .timeout(const Duration(seconds: 6));
              if (cancelled) {
                tls.destroy();
                break;
              }
              return tls;
            } on SocketException catch (e) {
              last = e;
            } on HandshakeException catch (e) {
              // e.g. a stale cached IP now serving someone else's site
              last = e;
            } on TlsException catch (e) {
              last = e;
            } on TimeoutException catch (e) {
              last = e;
            } finally {
              pending = null;
            }
            s?.destroy();
          }
          if (InternetAddress.tryParse(uri.host) != null) break;
        }
        throw _ConnectFailed(last ?? SocketException('no address for ${uri.host}'));
      }

      void cancel() {
        cancelled = true;
        pending?.destroy();
      }

      final socket = open().timeout(const Duration(seconds: 10), onTimeout: () {
        cancel();
        throw _ConnectFailed(TimeoutException('connect ${uri.host}'));
      });
      return ConnectionTask.fromSocket(socket, cancel);
    };
    return c;
  }

  static Future<List<InternetAddress>> _resolve(String host) async {
    final ip = InternetAddress.tryParse(host);
    if (ip != null) return [ip];
    final hit = _cache[host] ?? _load(host);
    if (hit != null) {
      if (hit.$2.millisecondsSinceEpoch == 0) {
        // From disk, first use since start: the server may have moved since
        // (e.g. the app was not opened for weeks), so give a fresh lookup a
        // moment before falling back to the saved address.
        try {
          return await _lookup(host).timeout(const Duration(milliseconds: 1500));
        } catch (_) {
          // Wait only once: from now on refresh in the background.
          _cache[host] = (hit.$1, DateTime.now().subtract(_fresh * 2));
          unawaited(_lookup(host).then((_) {}, onError: (_) {}));
          return hit.$1;
        }
      }
      // Stale in memory: use it now, refresh in the background.
      if (DateTime.now().difference(hit.$2) > _fresh) unawaited(_lookup(host).then((_) {}, onError: (_) {}));
      return hit.$1;
    }
    return _lookup(host);
  }

  // Any address family: IPv6-only / NAT64 networks (common on iOS) need AAAA
  // or synthesized addresses; IPv4 is tried first when both exist.
  static Future<List<InternetAddress>> _lookup(String host) =>
      _inflight[host] ??= InternetAddress.lookup(host)
          .then((l) => [
                ...l.where((a) => a.type == InternetAddressType.IPv4),
                ...l.where((a) => a.type != InternetAddressType.IPv4),
              ])
          .timeout(const Duration(seconds: 5))
          .then((list) {
        if (list.isEmpty) throw SocketException('no address for $host');
        _cache[host] = (list, DateTime.now());
        _save();
        return list;
      }).whenComplete(() => _inflight.remove(host));

  static (List<InternetAddress>, DateTime)? _load(String host) {
    try {
      final m = jsonDecode(Prefs.get(_prefsKey) ?? '{}') as Map<String, dynamic>;
      final ips = (m[host] as List<dynamic>?)?.map((e) => InternetAddress(e as String)).toList();
      if (ips == null || ips.isEmpty) return null;
      // From disk: usable right away, but refreshed on first use.
      return _cache[host] = (ips, DateTime.fromMillisecondsSinceEpoch(0));
    } catch (_) {
      return null;
    }
  }

  static void _save() {
    final m = {for (final e in _cache.entries) e.key: [for (final a in e.value.$1) a.address]};
    unawaited(Prefs.set(_prefsKey, jsonEncode(m)));
  }
}
