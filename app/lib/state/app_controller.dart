import 'dart:async';
import 'dart:io';
import 'dart:math';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

import '../core/analytics.dart';
import '../core/api.dart';
import '../core/config.dart';
import '../core/device.dart';
import '../core/iap.dart';
import '../core/prefs.dart';
import '../core/node_quality.dart';
import '../core/session.dart';
import '../core/updater.dart';
import '../core/vpn.dart';
import '../i18n/i18n.dart';

/// Connection modes offered in the app (must match server profile modes).
enum ConnectMode {
  direct('direct', '智能', '国内网站直连，海外经所选节点'),
  cluster('cluster', '集群', '多个节点自动择优，断线自动切换'),
  ai('ai', 'AI 优先', 'AI 服务走代理，自动选用最稳定的节点');

  const ConnectMode(this.id, this._label, this._hint);
  final String id;
  final String _label;
  final String _hint;
  String get label => tr(_label);
  String get hint => tr(_hint);

  static ConnectMode parse(String s) => values.firstWhere((m) => m.id == s, orElse: () => direct);
}

/// Result of a connect attempt, for the UI to react to.
enum ConnectResult { ok, needPlan, failed, cancelled }

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
    // iOS routes the app's own sockets into the tunnel, so probes would time
    // the local tun stack, not the path to each node: keep the last results.
    if (Platform.isIOS && active) return;
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

  /// The connect in progress can be stopped (tap the button again).
  bool get canCancel => status == VpnStatus.connecting && !cancelling;

  /// True between "cancel" and the tunnel being fully down.
  bool cancelling = false;

  // Incremented by every connect and every cancel / disconnect; a connect
  // attempt that sees a newer value has been cancelled and stops quietly.
  int _attempt = 0;
  int? _preNativeAttempt; // our connect attempt still loading the profile (nothing native yet)
  bool get active => status == VpnStatus.connected || status == VpnStatus.connecting;

  Future<void> init() async {
    // Tell the server which platform/build this is, so it serves the right
    // payment face (iOS App Review, Google Play, or normal web) on /plans and
    // /apps. The Google Play build reports a distinct platform so the server
    // always serves the Play Billing face.
    api.clientPlatform = playStore ? 'android-play' : Device.platform;
    // Awaited: /plans and /apps sent without the build would get the wrong face.
    try {
      api.clientBuild = Updater.buildOf(await Updater.info());
    } catch (_) {}
    // App Store transactions can arrive at any time (Ask to Buy, a purchase
    // left unfinished last run): handled for the whole app run.
    Iap.listen(api, onGranted: refreshUser);
    _statusSub = Vpn.statusStream().listen(_onStatus, onError: (_) {});
    try {
      status = await Vpn.status();
      if (status == VpnStatus.connected || status == VpnStatus.connecting) {
        // The tunnel outlived the previous app run (the VPN keeps running when
        // the app is closed): it is what the user wants, so it is kept up
        // (auto-reconnect) like one connected in this run.
        _wantConnected = true;
        _wasConnected = status == VpnStatus.connected;
      } else {
        await _checkInterruption();
      }
    } on PlatformException {
      // No native side (e.g. widget tests): stay disconnected.
    } on MissingPluginException {
      // same
    }
    mode = ConnectMode.parse(await session.mode());
    nodeId = await session.nodeId();
    final token = await session.token();
    api.token = token;
    api.keys = session.keys();
    // Token saved by an app version from before session keys: upgrade it in
    // the background (email accounts stay signed in; the old token keeps
    // working meanwhile). Offline / old server: tried again next start.
    if (token != null && api.keys == null) unawaited(_upgradeToken());
    if (token != null) {
      // Show the last known account and nodes right away (no spinner on a
      // normal start); the refresh below replaces them within a moment.
      final snap = session.snapshot();
      if (snap != null) {
        try {
          user = User.fromJson(snap.$1);
          nodes = [for (final e in snap.$2) Node.fromJson(e as Map<String, dynamic>)];
          if (nodeId == null || !nodes.any((n) => n.id == nodeId)) {
            nodeId = nodes.isEmpty ? null : nodes.first.id;
          }
        } catch (_) {
          user = null;
          nodes = [];
        }
      }
      loggedIn = true;
      notifyListeners();
      await refresh();
    } else {
      // No token yet: silently provision / recognize this device. No login screen.
      notifyListeners();
      await deviceSignIn();
    }
    _resumeTunnel();
    unawaited(_loadFace());
    // Keep traffic / node health current while the app is open.
    _refreshTimer = Timer.periodic(const Duration(minutes: 1), (_) {
      if (loggedIn == true && foreground) refresh(quiet: true);
    });
  }

  // ---- tunnel stopped while the app was away (Android) ----

  /// The phone's background management closed the app while the VPN was on
  /// (found at this start): the home page points to the 后台运行 settings.
  bool backgroundNotice = false;
  bool _resumeAfterStop = false;
  static const _kBackgroundNotice = 'bg_notice_at';

  /// The tunnel was on when the app last ran and nobody turned it off: the
  /// system closed it (background cleanup, low memory), it crashed, the app
  /// was updated or the phone restarted. It comes back once signed in.
  Future<void> _checkInterruption() async {
    final stop = await Vpn.takeInterruption();
    if (stop == null) return;
    Analytics.track('vpn_stop_${stop.reason}');
    if (stop.reason == 'crash') {
      unawaited(Analytics.reportError('VPN process exited (${stop.detail ?? 'no exit record'})', null, kind: 'native'));
    }
    // Force-stopped in the system settings: the user's choice.
    if (stop.reason == 'user') return;
    _resumeAfterStop = true;
    if (stop.bySystem) backgroundNotice = _backgroundNoticeDue();
  }

  /// At most once a day: on a phone that keeps closing the app it would nag.
  bool _backgroundNoticeDue() {
    final now = DateTime.now().millisecondsSinceEpoch;
    final last = int.tryParse(Prefs.get(_kBackgroundNotice) ?? '') ?? 0;
    if (now - last < const Duration(hours: 24).inMilliseconds) return false;
    Prefs.set(_kBackgroundNotice, '$now');
    return true;
  }

  void dismissBackgroundNotice() {
    backgroundNotice = false;
    notifyListeners();
  }

  void _resumeTunnel() {
    if (!_resumeAfterStop || loggedIn != true) return;
    _resumeAfterStop = false;
    if (active) return;
    _wasConnected = true; // retried with backoff like a tunnel that dropped
    unawaited(connect(auto: true));
  }

  Future<void> _upgradeToken() async {
    try {
      final r = await api.rekey();
      if (r.keys == null) return;
      await session.saveAuth(r);
      api.token = r.token;
      api.keys = r.keys;
    } catch (_) {}
  }

  /// The invite landing page copies "环游邀请码 ABCDEFG" before the download
  /// starts. On first launch we look for that marker so the invite is credited
  /// without the user typing anything. The server ignores it for existing accounts.
  static final _invitePattern = RegExp(r'环游邀请码\s*[:：]?\s*([A-Z2-9]{7})');

  Future<String?> _clipboardInviteCode() async {
    try {
      // hasStrings() does not trigger the iOS "Allow Paste" prompt; getData does.
      if (!await Clipboard.hasStrings()) return null;
      final text = (await Clipboard.getData(Clipboard.kTextPlain))?.text;
      return text == null ? null : _invitePattern.firstMatch(text)?.group(1);
    } catch (_) {
      return null;
    }
  }

  /// Device-based sign-in (the default, no-login path). Retries with backoff
  /// on network failure so a cold start behind a captive portal still lands.
  Future<void> deviceSignIn() => _signingIn ??= _deviceSignIn().whenComplete(() => _signingIn = null);
  Future<void>? _signingIn;

  // Read once, on the very first sign-in of this install (no device id yet):
  // later sign-ins (retries, logout, an expired token) must not read the
  // clipboard again (on iOS every read can show the system paste prompt).
  Future<String?>? _invite;

  Future<void> _deviceSignIn() async {
    final wasIn = loggedIn == true;
    if (loadFailed && !wasIn) {
      // The splash shows the attempt is running again ("重试" tapped / timer).
      loadFailed = false;
      message = null;
      notifyListeners();
    }
    try {
      _invite ??= session.hasDeviceId ? Future.value(null) : _clipboardInviteCode();
      final invite = await _invite;
      final id = await session.deviceId();
      final r = await api.device(id, Device.platform, inviteCode: invite);
      await session.saveAuth(r);
      api.token = r.token;
      api.keys = r.keys;
      user = r.user;
      loadFailed = false;
      _retryAttempt = 0;
      _retryTimer?.cancel();
      loggedIn = true;
      message = null;
      notifyListeners();
      await refresh();
      _resumeTunnel(); // the first sign-in of this start may have needed retries
    } catch (e) {
      // Any failure (network, unexpected response) retries with backoff:
      // the splash must never get stuck without a way forward. A user who
      // was already in (a re-auth after an expired token failed) stays on
      // the main screen: the tunnel keeps running and can still be stopped.
      if (!wasIn) {
        loggedIn = false;
        loadFailed = true;
      }
      message = e is ApiException && e.code != 'network' ? e.message : tr('网络连接失败，正在重试…');
      notifyListeners();
      _retryTimer?.cancel();
      final secs = min(30, 2 << min(_retryAttempt, 4));
      _retryAttempt++;
      _retryTimer = Timer(Duration(seconds: secs), () {
        // In the background the next resume retries instead (no wakeups).
        if (foreground && (loggedIn != true || api.token == null)) deviceSignIn();
      });
    }
  }

  @override
  void dispose() {
    _statusSub?.cancel();
    _refreshTimer?.cancel();
    _stableTimer?.cancel();
    _disconnectWatchdog?.cancel();
    _retryTimer?.cancel();
    _reconnectTimer?.cancel();
    super.dispose();
  }

  void _onStatus((VpnStatus, String?) e) {
    final prev = status;
    status = e.$1;
    // Native messages are Chinese source strings: translated like the app's own.
    final native = e.$2 == null || e.$2 == 'revoked' ? e.$2 : tr(e.$2!);
    e = (e.$1, native);
    statusHint = e.$1 == VpnStatus.connecting ? e.$2 : null;
    // After a cancel, errors from the half-started tunnel are not news.
    if (cancelling && (e.$1 == VpnStatus.error || e.$1 == VpnStatus.disconnected)) {
      cancelling = false;
      status = VpnStatus.disconnected;
      notifyListeners();
      return;
    }
    // Stopped outside the app (notification "断开", system settings, another
    // VPN app): respect it, no auto-reconnect.
    if (e.$1 == VpnStatus.disconnecting && prev != VpnStatus.disconnecting && !_stopRequested) {
      _wantConnected = false;
    }
    if (e.$1 == VpnStatus.disconnected && e.$2 == 'revoked') {
      _wantConnected = false;
      _reconnectTimer?.cancel();
      message = tr('VPN 已被系统或其它 VPN 应用断开');
    }
    if (e.$1 == VpnStatus.disconnected || e.$1 == VpnStatus.error) _stopRequested = false;
    if (e.$1 == VpnStatus.error) message = e.$2 ?? tr('连接失败');
    // Counted when a connect of this run succeeds, not when a reopened app
    // finds the tunnel already up.
    if (e.$1 == VpnStatus.connected && prev == VpnStatus.connecting) Analytics.track('vpn_connected');
    if (e.$1 == VpnStatus.error && prev == VpnStatus.connecting) Analytics.track('vpn_connect_failed');
    if (e.$1 == VpnStatus.connected) {
      message = null;
      connectedAt ??= Vpn.connectedSince ?? DateTime.now();
      _wasConnected = true;
      _reconnectTimer?.cancel();
      // Reset the reconnect backoff only once the tunnel has stayed up: a
      // tunnel that connects and drops right away must not loop every 3 s.
      _stableTimer?.cancel();
      _stableTimer = Timer(const Duration(seconds: 60), () => _reconnectAttempt = 0);
    } else if (e.$1 != VpnStatus.connecting) {
      connectedAt = null;
      _stableTimer?.cancel();
    }
    // The tunnel went down without the user asking: bring it back.
    final dropped = e.$1 == VpnStatus.error || (e.$1 == VpnStatus.disconnected && prev != VpnStatus.disconnecting);
    // Only for a tunnel that was actually up: a first connect that fails
    // (bad config, permission) is reported, not retried in a loop.
    if (dropped && _wantConnected && _wasConnected) {
      if (e.$2 == 'revoked') {
        // Another VPN app took over or permission was revoked: respect that.
        _wantConnected = false;
        message = tr('VPN 已被系统或其它 VPN 应用断开');
      } else {
        _scheduleReconnect();
      }
    }
    notifyListeners();
  }

  // ---- auto-reconnect ----
  // Network switches are handled natively (the tunnel is rebuilt on the new
  // network). This covers the rest: the service being killed, a failed start,
  // or the core stopping. Backoff 3 s → 6 → 12 → 24 → 48 → 60 s, max 8 tries.

  /// Extra status text from the native side while connecting (e.g. waiting for network).
  String? statusHint;
  bool _wantConnected = false;
  bool _wasConnected = false; // the tunnel reached "connected" since the user connected
  bool _stopRequested = false; // the app itself asked the tunnel to stop
  Timer? _stableTimer;
  Timer? _disconnectWatchdog;

  /// False while the app is in the background (no periodic refresh then).
  bool foreground = true;
  int _reconnectAttempt = 0;
  Timer? _reconnectTimer;

  bool get reconnecting => (_reconnectTimer?.isActive ?? false) || _reconnectOnResume;
  bool _reconnectOnResume = false;

  void _scheduleReconnect() {
    _reconnectTimer?.cancel();
    if (_reconnectAttempt >= 8) {
      _wantConnected = false;
      message = tr('多次重连失败，请检查网络后手动连接');
      return;
    }
    final secs = min(60, 3 << _reconnectAttempt);
    _reconnectAttempt++;
    message = tr('连接已断开，{0} 秒后自动重连（第 {1} 次）', [secs, _reconnectAttempt]);
    _reconnectTimer = Timer(Duration(seconds: secs), () {
      if (!_wantConnected || active) return;
      // Android 12+ does not let a background app start the VPN service
      // (foreground-service start restriction): do it when the app is back.
      if (Platform.isAndroid && !foreground) {
        _reconnectOnResume = true;
        return;
      }
      connect(auto: true);
    });
  }

  // ---- auth ----

  Future<void> login(String email, String password) => _signIn(api.login(email, password));

  Future<void> register(String email, String password, {String? inviteCode}) =>
      _signIn(api.register(email, password, inviteCode: inviteCode));

  Future<void> _signIn(Future<AuthResult> f) async {
    final r = await f;
    await session.saveAuth(r);
    api.token = r.token;
    api.keys = r.keys;
    user = r.user;
    loggedIn = true;
    notifyListeners();
    await refresh();
  }

  Future<void> logout() async {
    if (active) await disconnect();
    await session.clear();
    api.token = null;
    api.keys = null;
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

  // ---- official app-store face (Apple App Store / Google Play) ----
  static const _kFace = 'iap_face';

  /// The Google Play build: fixed at compile time (channel=play on Android).
  /// The same for every user, never flipped by the server, so the Play build
  /// always complies with Google Play policy.
  static bool get playStore => Platform.isAndroid && AppConfig.isPlayStore;

  // iOS App Review face, driven by the server (ios_review_build): Apple in-app
  // purchase only. Cached so it is right from the first frame; unknown on iOS
  // counts as the review face.
  bool _iosFace = Platform.isIOS && Prefs.get(_kFace) != '0';

  /// Whether this client must behave like an official app-store build: buy
  /// only through the store, and point nowhere outside it to pay or download —
  /// no "send your ID to support to top up", no download page, no self-update,
  /// no recommended apps. True on the Google Play build and on iOS builds
  /// under App Review.
  bool get iapFace => playStore || _iosFace;

  void setIapFace(bool v) {
    // Only the iOS face is server-driven; the Play face is a compile-time constant.
    if (!Platform.isIOS || v == _iosFace) return;
    _iosFace = v;
    Prefs.set(_kFace, v ? '1' : '0');
    notifyListeners();
  }

  Future<void> _loadFace() async {
    if (!Platform.isIOS || loggedIn != true) return;
    try {
      final (_, _, purchase) = await api.plansFull();
      setIapFace(purchase.iap);
    } catch (_) {
      // keep the cached value; the plans page updates it too
    }
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
      final r = await Future.wait([api.getJson('/api/v1/me'), api.getJson('/api/v1/nodes')]);
      final nodeList = r[1]['nodes'] as List<dynamic>;
      user = User.fromJson(r[0]);
      nodes = [for (final e in nodeList) Node.fromJson(e as Map<String, dynamic>)];
      session.saveSnapshot(r[0], nodeList);
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
      if (e.unauthorized) {
        // Not awaited: the re-auth ends with a refresh(), which would get
        // this very (unfinished) refresh back and wait on itself forever.
        Timer.run(_onUnauthorized);
        return;
      }
      if (user == null || nodes.isEmpty) {
        loadFailed = true;
        _scheduleRetry();
      }
      if (!quiet) message = e.code == 'network' ? tr('网络连接失败，正在重试…') : e.message;
      notifyListeners();
    } catch (_) {
      // Unexpected response shape: keep what we have, try again later.
      if (user == null || nodes.isEmpty) {
        loadFailed = true;
        _scheduleRetry();
      }
      notifyListeners();
    }
  }

  DateTime? _lastReauth;

  /// The session token was refused (expired, server key rotated). Get a new
  /// one for this device in the background; a running tunnel is left alone
  /// (it does not depend on the token). At most once per 2 minutes, so a
  /// server that keeps refusing can't cause a sign-in loop.
  Future<void> _onUnauthorized() async {
    final now = DateTime.now();
    if (_lastReauth != null && now.difference(_lastReauth!) < const Duration(minutes: 2)) {
      message = tr('登录状态异常，请稍后重试');
      notifyListeners();
      return;
    }
    _lastReauth = now;
    api.token = null;
    api.keys = null;
    await deviceSignIn();
  }

  void _scheduleRetry() {
    _retryTimer?.cancel();
    final secs = min(30, 2 << min(_retryAttempt, 4)); // 2, 4, 8, 16, 30…
    _retryAttempt++;
    _retryTimer = Timer(Duration(seconds: secs), () {
      if (loggedIn == true && foreground) refresh(quiet: true);
    });
  }

  /// Called when the app returns to the foreground.
  void onResumed() {
    foreground = true;
    if (_reconnectOnResume) {
      _reconnectOnResume = false;
      if (_wantConnected && !active) connect(auto: true);
    }
    if (loggedIn == true && api.token != null) {
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
      if (e.unauthorized) await _onUnauthorized();
    } catch (_) {}
  }

  // ---- preferences ----

  /// Picks a node. If [connectNow] (the default from the node page), the tunnel
  /// is (re)connected to it right away. Picking a node leaves cluster mode.
  Future<ConnectResult?> selectNode(String id, {bool connectNow = true}) async {
    Analytics.track('node_select');
    // Picking a node by hand means "use this one": leave the automatic modes.
    final changed = id != nodeId || mode == ConnectMode.cluster || mode == ConnectMode.ai;
    nodeId = id;
    if (mode == ConnectMode.cluster || mode == ConnectMode.ai) {
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
    Analytics.track('mode_${m.id}');
    mode = m;
    await session.saveMode(m.id);
    notifyListeners();
    if (active) return reconnect();
    return null;
  }

  // ---- tunnel ----

  /// The power button: connect, cancel a connect in progress, or disconnect.
  Future<ConnectResult?> toggle() async {
    if (status == VpnStatus.connecting) {
      await cancelConnect();
      return ConnectResult.cancelled;
    }
    if (active) {
      await disconnect();
      return null;
    }
    return connect();
  }

  /// Stops a connect in progress. Before the tunnel is started (still loading
  /// the profile) nothing native has happened yet, so it is instant.
  Future<void> cancelConnect() async {
    if (status != VpnStatus.connecting) return;
    Analytics.track('vpn_connect_cancel');
    // Only a connect of ours that is still loading the profile can be
    // dropped silently; any other "connecting" (ours after Vpn.connect, or
    // the native side reconnecting on its own) has to stop the tunnel.
    final startedNative = _preNativeAttempt != _attempt;
    _attempt++;
    _wantConnected = false;
    _wasConnected = false;
    _reconnectTimer?.cancel();
    _reconnectOnResume = false;
    _reconnectAttempt = 0;
    message = null;
    if (!startedNative) {
      status = VpnStatus.disconnected;
      notifyListeners();
      return;
    }
    cancelling = true;
    _stopRequested = true;
    status = VpnStatus.disconnecting;
    notifyListeners();
    try {
      await Vpn.disconnect();
    } on PlatformException {
      // reported as stopped below
    }
    if (status != VpnStatus.disconnected) {
      await _waitFor(VpnStatus.disconnected, const Duration(seconds: 6));
    }
    cancelling = false;
    status = VpnStatus.disconnected;
    notifyListeners();
  }

  Future<ConnectResult> reconnect() async {
    // Desktop switches inside the running tunnel: no disconnect in between.
    if (Vpn.canSwitchLive && status == VpnStatus.connected) return connect(switching: true);
    if (active) {
      await disconnect();
      await _waitFor(VpnStatus.disconnected, const Duration(seconds: 6));
    }
    return connect();
  }

  /// Connects. [auto] marks a background reconnect attempt: on failure it
  /// schedules the next attempt instead of giving up.
  Future<ConnectResult> connect({bool auto = false, bool switching = false}) async {
    if (!auto) Analytics.track('vpn_connect');
    if (!auto) {
      _reconnectAttempt = 0;
      _reconnectTimer?.cancel();
    }
    _wantConnected = true;
    _stopRequested = false;
    final r = await _connectOnce(switching: switching);
    if (r == ConnectResult.needPlan) {
      _wantConnected = false;
    } else if (r == ConnectResult.failed) {
      if (auto && _wantConnected && _wasConnected) {
        _scheduleReconnect();
        notifyListeners();
      } else if (!auto) {
        _wantConnected = false;
      }
    }
    return r;
  }

  /// Fetches a fresh profile and updates the cache, without touching the live
  /// tunnel. Used after a fast-path connect so the next connect is current.
  Future<void> _refreshProfile(String? node, String modeId) async {
    try {
      await session.saveProfile(await api.profile(nodeId: node, mode: modeId));
    } catch (_) {
      // keep the cached one; the next connect tries again
    }
  }

  /// [switching]: the tunnel is up and only gets another node / mode (desktop).
  Future<ConnectResult> _connectOnce({bool switching = false}) async {
    final attempt = ++_attempt;
    // A cancel while switching stops the running tunnel.
    _preNativeAttempt = switching ? null : attempt;
    bool cancelled() => attempt != _attempt;
    status = VpnStatus.connecting;
    cancelling = false;
    message = null;
    notifyListeners();
    // Nodes never loaded (e.g. first launch was offline): load them before connecting.
    if (nodes.isEmpty || user == null) await refresh();
    if (cancelled()) return ConnectResult.cancelled;
    try {
      Profile profile;
      // Cluster and AI 优先 let the server choose (AI picks the most stable nodes).
      final node = mode == ConnectMode.cluster || mode == ConnectMode.ai ? null : selectedNode?.id;
      final cached = await session.cachedProfile();
      final sameTarget = cached != null && cached.mode == mode.id && (node == null || cached.node.id == node);
      final age = session.profileAge();
      // Fast path: a very recent profile for the same target. The compiled
      // profile can be large (the China routing table), so re-downloading it
      // on every (re)connect is the usual cause of a slow connect on a poor
      // network. Connect with the cached one now and refresh it in the
      // background. Kept short (10 min) so a profile change (node UUID / set)
      // is picked up quickly; longer-lived staleness only matters after admin
      // changes, which are rare.
      if (sameTarget && age != null && age < const Duration(minutes: 10)) {
        profile = cached;
        unawaited(_refreshProfile(node, mode.id));
      } else {
        try {
          profile = await api.profile(nodeId: node, mode: mode.id);
          await session.saveProfile(profile);
        } on ApiException catch (e) {
          if (e.unauthorized) {
            await _onUnauthorized();
            if (cancelled()) return ConnectResult.cancelled;
            status = VpnStatus.disconnected;
            message = tr('登录已刷新，请再点一次连接');
            notifyListeners();
            return ConnectResult.failed;
          }
          // API unreachable: reuse the last profile for the same node and mode.
          if (e.code != 'network' || !sameTarget) rethrow;
          profile = cached;
        }
      }
      if (cancelled()) return ConnectResult.cancelled;
      // From here a cancel has to stop the native tunnel (on mobile it keeps
      // connecting after Vpn.connect returns).
      _preNativeAttempt = null;
      await Vpn.connect(profile.config, name: profile.node.name);
      if (cancelled()) return ConnectResult.cancelled;
      return ConnectResult.ok;
    } on ApiException catch (e) {
      if (cancelled()) return ConnectResult.cancelled;
      // Switching failed before touching the tunnel: the old line keeps working.
      status = switching ? VpnStatus.connected : VpnStatus.disconnected;
      message = e.message;
      notifyListeners();
      if (e.status == 402) {
        await refreshUser();
        return ConnectResult.needPlan;
      }
      return ConnectResult.failed;
    } on PlatformException catch (e) {
      if (cancelled() || e.code == 'cancelled') return ConnectResult.cancelled;
      status = VpnStatus.disconnected;
      message = e.message != null ? tr(e.message!) : tr('无法启动 VPN');
      notifyListeners();
      return ConnectResult.failed;
    }
  }

  Future<void> disconnect() async {
    if (status == VpnStatus.connecting) return cancelConnect();
    Analytics.track('vpn_disconnect');
    _attempt++;
    _wantConnected = false;
    _wasConnected = false;
    _reconnectTimer?.cancel();
    _reconnectOnResume = false;
    _reconnectAttempt = 0;
    _stopRequested = true;
    status = VpnStatus.disconnecting;
    notifyListeners();
    try {
      await Vpn.disconnect();
    } on PlatformException {
      status = VpnStatus.disconnected;
      notifyListeners();
    }
    // The native side reports when the tunnel is down; if that report never
    // comes (iOS without a tunnel manager yet, a dead service), don't leave
    // the UI stuck on 断开中.
    _disconnectWatchdog?.cancel();
    _disconnectWatchdog = Timer(const Duration(seconds: 8), () async {
      if (status != VpnStatus.disconnecting) return;
      VpnStatus s = VpnStatus.disconnected;
      try {
        s = await Vpn.status();
      } catch (_) {}
      if (status != VpnStatus.disconnecting) return;
      status = s == VpnStatus.connected ? VpnStatus.connected : VpnStatus.disconnected;
      notifyListeners();
    });
  }

  /// Called when the app goes to the background.
  void onPaused() => foreground = false;

  Future<void> _waitFor(VpnStatus target, Duration timeout) async {
    final end = DateTime.now().add(timeout);
    while (status != target && DateTime.now().isBefore(end)) {
      await Future<void>.delayed(const Duration(milliseconds: 150));
    }
  }
}
