import 'dart:async';
import 'dart:math';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

import '../core/api.dart';
import '../core/device.dart';
import '../core/node_quality.dart';
import '../core/session.dart';
import '../core/vpn.dart';

/// Connection modes offered in the app (must match server profile modes).
enum ConnectMode {
  direct('direct', '智能', '国内网站直连，海外经所选节点'),
  cluster('cluster', '集群', '多个节点自动择优，断线自动切换'),
  ai('ai', 'AI', '只有 ChatGPT、Claude 等 AI 服务走代理');

  const ConnectMode(this.id, this.label, this.hint);
  final String id;
  final String label;
  final String hint;

  static ConnectMode parse(String s) => values.firstWhere((m) => m.id == s, orElse: () => direct);
}

/// Result of a connect attempt, for the UI to react to.
enum ConnectResult { ok, needPlan, failed }

/// Single source of truth for app state: auth, user, nodes, chosen mode/node
/// and the VPN tunnel. Widgets read it via AppScope.of(context) and call its
/// methods; they never talk to ApiClient / Vpn directly for this state.
class AppController extends ChangeNotifier {
  AppController({ApiClient? api, Session? session})
      : api = api ?? ApiClient(),
        session = session ?? Session();

  final ApiClient api;
  final Session session;

  // ---- auth / account ----
  bool? loggedIn; // null while starting up
  User? user;

  // ---- nodes / preferences ----
  List<Node> nodes = [];
  String? nodeId;
  ConnectMode mode = ConnectMode.direct;

  // ---- tunnel ----
  VpnStatus status = VpnStatus.disconnected;
  String? message; // last user-facing error
  DateTime? connectedAt;
  StreamSubscription<(VpnStatus, String?)>? _statusSub;
  Timer? _refreshTimer;

  // ---- node quality (measured from this device) ----
  final Map<String, NodeQuality> quality = {};
  bool probing = false;
  DateTime? probedAt;

  NodeScore scoreOf(Node n) => NodeScore(n, quality[n.id]);

  /// Nodes ordered best first.
  List<NodeScore> get rankedNodes =>
      [for (final n in nodes) scoreOf(n)]..sort((a, b) => a.value.compareTo(b.value));

  /// The best reachable node, or null if none measured yet.
  NodeScore? get bestNode {
    final r = rankedNodes;
    return r.isNotEmpty && r.first.value.isFinite && r.first.quality != null ? r.first : null;
  }

  /// Background quality check after the node list loads. Until the user picks a
  /// node themselves, the default selection follows the best-measured node
  /// (only while disconnected, so a working tunnel is never switched under them).
  Future<void> _probeAndAutoPick() async {
    await probeNodes();
    final best = bestNode;
    if (best == null || await session.nodeId() != null) return;
    if (!active && mode != ConnectMode.cluster && nodeId != best.node.id) {
      nodeId = best.node.id;
      notifyListeners();
    }
  }

  /// Measures every node from this device. Skips if a run is in progress or
  /// one finished within [minInterval] (unless forced).
  Future<void> probeNodes({bool force = false, Duration minInterval = const Duration(minutes: 2)}) async {
    if (probing || nodes.isEmpty) return;
    if (!force && probedAt != null && DateTime.now().difference(probedAt!) < minInterval) return;
    probing = true;
    notifyListeners();
    try {
      await NodeProber.probeAll(nodes, (id, q) {
        quality[id] = q;
        notifyListeners();
      });
      probedAt = DateTime.now();
    } finally {
      probing = false;
      notifyListeners();
    }
  }

  Node? get selectedNode {
    for (final n in nodes) {
      if (n.id == nodeId) return n;
    }
    return nodes.isEmpty ? null : nodes.first;
  }

  bool get busy => status == VpnStatus.connecting || status == VpnStatus.disconnecting;
  bool get active => status == VpnStatus.connected || status == VpnStatus.connecting;

  Future<void> init() async {
    _statusSub = Vpn.statusStream().listen(_onStatus, onError: (_) {});
    try {
      status = await Vpn.status();
    } on PlatformException {
      // No native side (e.g. widget tests): stay disconnected.
    } on MissingPluginException {
      // same
    }
    mode = ConnectMode.parse(await session.mode());
    nodeId = await session.nodeId();
    final token = await session.token();
    api.token = token;
    if (token != null) {
      loggedIn = true;
      notifyListeners();
      await refresh();
    } else {
      // No token yet: silently provision / recognize this device. No login screen.
      notifyListeners();
      await deviceSignIn();
    }
    // Keep traffic / node health current while the app is open.
    _refreshTimer = Timer.periodic(const Duration(minutes: 1), (_) {
      if (loggedIn == true) refresh(quiet: true);
    });
  }

  /// The invite landing page copies "环游邀请码 ABCDEFG" before the download
  /// starts. On first launch we look for that marker so the invite is credited
  /// without the user typing anything. The server ignores it for existing accounts.
  static final _invitePattern = RegExp(r'环游邀请码\s*[:：]?\s*([A-Z2-9]{7})');

  Future<String?> _clipboardInviteCode() async {
    try {
      final text = (await Clipboard.getData(Clipboard.kTextPlain))?.text;
      return text == null ? null : _invitePattern.firstMatch(text)?.group(1);
    } catch (_) {
      return null;
    }
  }

  /// Device-based sign-in (the default, no-login path). Retries with backoff
  /// on network failure so a cold start behind a captive portal still lands.
  Future<void> deviceSignIn() async {
    try {
      final id = await session.deviceId();
      final r = await api.device(id, Device.platform, inviteCode: await _clipboardInviteCode());
      await session.saveToken(r.token);
      api.token = r.token;
      user = r.user;
      loadFailed = false;
      _retryAttempt = 0;
      _retryTimer?.cancel();
      loggedIn = true;
      message = null;
      notifyListeners();
      await refresh();
    } on ApiException catch (e) {
      loggedIn = false;
      loadFailed = true;
      message = e.code == 'network' ? '网络连接失败，正在重试…' : e.message;
      notifyListeners();
      _retryTimer?.cancel();
      final secs = min(30, 2 << min(_retryAttempt, 4));
      _retryAttempt++;
      _retryTimer = Timer(Duration(seconds: secs), () {
        if (loggedIn != true) deviceSignIn();
      });
    }
  }

  @override
  void dispose() {
    _statusSub?.cancel();
    _refreshTimer?.cancel();
    _retryTimer?.cancel();
    super.dispose();
  }

  void _onStatus((VpnStatus, String?) e) {
    status = e.$1;
    if (e.$1 == VpnStatus.error) message = e.$2 ?? '连接失败';
    if (e.$1 == VpnStatus.connected) {
      message = null;
      connectedAt ??= DateTime.now();
    } else if (e.$1 != VpnStatus.connecting) {
      connectedAt = null;
    }
    notifyListeners();
  }

  // ---- auth ----

  Future<void> login(String email, String password) => _signIn(api.login(email, password));

  Future<void> register(String email, String password, {String? inviteCode}) =>
      _signIn(api.register(email, password, inviteCode: inviteCode));

  Future<void> _signIn(Future<AuthResult> f) async {
    final r = await f;
    await session.saveToken(r.token);
    api.token = r.token;
    user = r.user;
    loggedIn = true;
    notifyListeners();
    await refresh();
  }

  Future<void> logout() async {
    if (active) await disconnect();
    await session.clear();
    api.token = null;
    user = null;
    nodes = [];
    loadFailed = false;
    _retryTimer?.cancel();
    _retryAttempt = 0;
    loggedIn = false;
    notifyListeners();
    // Device accounts are automatic: immediately re-provision so the app stays
    // usable without a login screen. (An expired token lands here too.)
    await deviceSignIn();
  }

  /// True while user / nodes failed to load and a retry is pending.
  bool loadFailed = false;
  Timer? _retryTimer;
  int _retryAttempt = 0;
  Future<void>? _refreshing;

  /// Reloads user + node list. Signs out on an expired token. On a network
  /// error it retries with backoff (2 s → 30 s): on first launch iOS may
  /// still be asking "允许使用无线数据", and the first requests fail.
  Future<void> refresh({bool quiet = false}) => _refreshing ??= _doRefresh(quiet).whenComplete(() => _refreshing = null);

  Future<void> _doRefresh(bool quiet) async {
    try {
      final r = await Future.wait([api.me(), api.nodes()]);
      user = r[0] as User;
      nodes = r[1] as List<Node>;
      if (nodeId == null || !nodes.any((n) => n.id == nodeId)) {
        nodeId = nodes.isEmpty ? null : nodes.first.id;
      }
      loadFailed = false;
      _retryAttempt = 0;
      _retryTimer?.cancel();
      if (!quiet) message = null;
      notifyListeners();
      unawaited(_probeAndAutoPick());
    } on ApiException catch (e) {
      if (e.unauthorized) return logout();
      if (user == null || nodes.isEmpty) {
        loadFailed = true;
        _scheduleRetry();
      }
      if (!quiet) message = e.code == 'network' ? '网络连接失败，正在重试…' : e.message;
      notifyListeners();
    }
  }

  void _scheduleRetry() {
    _retryTimer?.cancel();
    final secs = min(30, 2 << min(_retryAttempt, 4)); // 2, 4, 8, 16, 30…
    _retryAttempt++;
    _retryTimer = Timer(Duration(seconds: secs), () {
      if (loggedIn == true) refresh(quiet: true);
    });
  }

  /// Called when the app returns to the foreground.
  void onResumed() {
    if (loggedIn == true) {
      refresh(quiet: !loadFailed);
    } else {
      deviceSignIn();
    }
  }

  Future<void> refreshUser() async {
    try {
      user = await api.me();
      notifyListeners();
    } on ApiException catch (e) {
      if (e.unauthorized) await logout();
    }
  }

  // ---- preferences ----

  /// Picks a node. If [connectNow] (the default from the node page), the tunnel
  /// is (re)connected to it right away. Picking a node leaves cluster mode.
  Future<ConnectResult?> selectNode(String id, {bool connectNow = true}) async {
    final changed = id != nodeId || mode == ConnectMode.cluster;
    nodeId = id;
    if (mode == ConnectMode.cluster) {
      mode = ConnectMode.direct;
      await session.saveMode(mode.id);
    }
    await session.saveNodeId(id);
    notifyListeners();
    if (!connectNow) return null;
    if (status == VpnStatus.connected && !changed) return ConnectResult.ok;
    return reconnect();
  }

  /// Changes the mode; reconnects if the tunnel is up.
  Future<ConnectResult?> setMode(ConnectMode m) async {
    if (m == mode) return null;
    mode = m;
    await session.saveMode(m.id);
    notifyListeners();
    if (active) return reconnect();
    return null;
  }

  // ---- tunnel ----

  Future<ConnectResult?> toggle() => active ? disconnect().then((_) => null) : connect();

  Future<ConnectResult> reconnect() async {
    if (active) {
      await disconnect();
      await _waitFor(VpnStatus.disconnected, const Duration(seconds: 6));
    }
    return connect();
  }

  Future<ConnectResult> connect() async {
    status = VpnStatus.connecting;
    message = null;
    notifyListeners();
    // Nodes never loaded (e.g. first launch was offline): load them before connecting.
    if (nodes.isEmpty || user == null) await refresh();
    try {
      Profile profile;
      final node = mode == ConnectMode.cluster ? null : selectedNode?.id;
      try {
        profile = await api.profile(nodeId: node, mode: mode.id);
        await session.saveProfile(profile);
      } on ApiException catch (e) {
        if (e.unauthorized) {
          await logout();
          return ConnectResult.failed;
        }
        // API unreachable: reuse the last profile for the same node and mode.
        final cached = await session.cachedProfile();
        final sameTarget = cached != null && cached.mode == mode.id && (node == null || cached.node.id == node);
        if (e.code != 'network' || !sameTarget) rethrow;
        profile = cached;
      }
      await Vpn.connect(profile.config, name: profile.node.name);
      return ConnectResult.ok;
    } on ApiException catch (e) {
      status = VpnStatus.disconnected;
      message = e.message;
      notifyListeners();
      if (e.status == 402) {
        await refreshUser();
        return ConnectResult.needPlan;
      }
      return ConnectResult.failed;
    } on PlatformException catch (e) {
      status = VpnStatus.disconnected;
      message = e.message ?? '无法启动 VPN';
      notifyListeners();
      return ConnectResult.failed;
    }
  }

  Future<void> disconnect() async {
    status = VpnStatus.disconnecting;
    notifyListeners();
    try {
      await Vpn.disconnect();
    } on PlatformException {
      status = VpnStatus.disconnected;
      notifyListeners();
    }
  }

  Future<void> _waitFor(VpnStatus target, Duration timeout) async {
    final end = DateTime.now().add(timeout);
    while (status != target && DateTime.now().isBefore(end)) {
      await Future<void>.delayed(const Duration(milliseconds: 150));
    }
  }
}
