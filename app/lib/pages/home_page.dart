import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../api.dart';
import '../session.dart';
import '../vpn.dart';

class HomePage extends StatefulWidget {
  const HomePage({super.key, required this.api, required this.session, required this.onLogout});
  final ApiClient api;
  final Session session;
  final Future<void> Function() onLogout;

  @override
  State<HomePage> createState() => _HomePageState();
}

class _HomePageState extends State<HomePage> {
  VpnStatus _status = VpnStatus.disconnected;
  String? _message;
  List<Node> _nodes = [];
  String? _nodeId;
  String _mode = 'smart';
  User? _user;
  StreamSubscription<(VpnStatus, String?)>? _sub;
  DateTime? _connectedAt;
  Timer? _ticker;

  @override
  void initState() {
    super.initState();
    _sub = Vpn.statusStream().listen((e) {
      setState(() {
        _status = e.$1;
        if (e.$1 == VpnStatus.error) _message = e.$2 ?? '连接失败';
        if (e.$1 == VpnStatus.connected) {
          _message = null;
          _connectedAt ??= DateTime.now();
        } else {
          _connectedAt = null;
        }
      });
    });
    _ticker = Timer.periodic(const Duration(seconds: 1), (_) {
      if (_connectedAt != null && mounted) setState(() {});
    });
    _load();
  }

  @override
  void dispose() {
    _sub?.cancel();
    _ticker?.cancel();
    super.dispose();
  }

  Future<void> _load() async {
    final status = await Vpn.status();
    final nodeId = await widget.session.nodeId();
    final mode = await widget.session.mode();
    setState(() {
      _status = status;
      _nodeId = nodeId;
      _mode = mode;
    });
    try {
      final results = await Future.wait([widget.api.me(), widget.api.nodes()]);
      setState(() {
        _user = results[0] as User;
        _nodes = results[1] as List<Node>;
        if (_nodeId == null || !_nodes.any((n) => n.id == _nodeId)) {
          _nodeId = _nodes.isEmpty ? null : _nodes.first.id;
        }
      });
    } on ApiException catch (e) {
      if (e.unauthorized) return widget.onLogout();
      setState(() => _message = e.message);
    }
  }

  Future<void> _toggle() async {
    if (_status == VpnStatus.connected || _status == VpnStatus.connecting) {
      setState(() => _status = VpnStatus.disconnecting);
      await Vpn.disconnect();
      return;
    }
    setState(() {
      _status = VpnStatus.connecting;
      _message = null;
    });
    try {
      Profile profile;
      try {
        profile = await widget.api.profile(nodeId: _nodeId, mode: _mode);
        await widget.session.saveProfile(profile);
      } on ApiException catch (e) {
        if (e.unauthorized) return widget.onLogout();
        // API unreachable: fall back to the last profile for the same node/mode.
        final cached = await widget.session.cachedProfile();
        if (e.code != 'network' || cached == null || cached.node.id != _nodeId || cached.mode != _mode) {
          rethrow;
        }
        profile = cached;
      }
      await Vpn.connect(profile.config, name: profile.node.name);
    } on ApiException catch (e) {
      setState(() {
        _status = VpnStatus.disconnected;
        _message = e.message;
      });
    } on PlatformException catch (e) {
      setState(() {
        _status = VpnStatus.disconnected;
        _message = e.message ?? '无法启动 VPN';
      });
    }
  }

  bool get _busy => _status == VpnStatus.connecting || _status == VpnStatus.disconnecting;
  bool get _locked => _status != VpnStatus.disconnected && _status != VpnStatus.error;

  String get _statusText => switch (_status) {
        VpnStatus.connected => '已连接  ${_elapsed()}',
        VpnStatus.connecting => '正在连接…',
        VpnStatus.disconnecting => '正在断开…',
        VpnStatus.error => '连接失败',
        VpnStatus.disconnected => '未连接',
      };

  String _elapsed() {
    if (_connectedAt == null) return '';
    final d = DateTime.now().difference(_connectedAt!);
    String two(int n) => n.toString().padLeft(2, '0');
    return '${two(d.inHours)}:${two(d.inMinutes % 60)}:${two(d.inSeconds % 60)}';
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final connected = _status == VpnStatus.connected;
    final color = connected ? const Color(0xFF1DB954) : theme.colorScheme.primary;

    return Scaffold(
      appBar: AppBar(
        title: const Text('安全连接'),
        actions: [
          PopupMenuButton<String>(
            onSelected: (v) async {
              if (v == 'logout') {
                if (_locked) await Vpn.disconnect();
                await widget.onLogout();
              }
            },
            itemBuilder: (_) => [
              if (_user != null) PopupMenuItem(enabled: false, child: Text(_user!.email)),
              const PopupMenuItem(value: 'logout', child: Text('退出登录')),
            ],
          ),
        ],
      ),
      body: SafeArea(
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(
            children: [
              const Spacer(),
              GestureDetector(
                onTap: _busy ? null : _toggle,
                child: AnimatedContainer(
                  duration: const Duration(milliseconds: 300),
                  width: 180,
                  height: 180,
                  decoration: BoxDecoration(
                    shape: BoxShape.circle,
                    color: color.withValues(alpha: connected ? 1 : 0.12),
                    boxShadow: [
                      if (connected) BoxShadow(color: color.withValues(alpha: 0.4), blurRadius: 40, spreadRadius: 4),
                    ],
                  ),
                  child: Center(
                    child: _busy
                        ? SizedBox.square(dimension: 56, child: CircularProgressIndicator(color: color, strokeWidth: 4))
                        : Icon(Icons.power_settings_new_rounded, size: 84, color: connected ? Colors.white : color),
                  ),
                ),
              ),
              const SizedBox(height: 28),
              Text(_statusText, style: theme.textTheme.titleLarge),
              if (_message != null) ...[
                const SizedBox(height: 12),
                Text(_message!, textAlign: TextAlign.center, style: TextStyle(color: theme.colorScheme.error)),
              ],
              if (_user != null && !_user!.active) ...[
                const SizedBox(height: 12),
                Text('账号已过期或被停用', style: TextStyle(color: theme.colorScheme.error)),
              ],
              const Spacer(),
              _NodePicker(
                nodes: _nodes,
                selected: _nodeId,
                enabled: !_locked,
                onChanged: (id) {
                  setState(() => _nodeId = id);
                  widget.session.saveNodeId(id);
                },
              ),
              const SizedBox(height: 12),
              SegmentedButton<String>(
                segments: const [
                  ButtonSegment(value: 'smart', label: Text('智能分流'), icon: Icon(Icons.alt_route)),
                  ButtonSegment(value: 'global', label: Text('全局代理'), icon: Icon(Icons.public)),
                ],
                selected: {_mode},
                onSelectionChanged: _locked
                    ? null
                    : (s) {
                        setState(() => _mode = s.first);
                        widget.session.saveMode(s.first);
                      },
              ),
              const SizedBox(height: 8),
            ],
          ),
        ),
      ),
    );
  }
}

class _NodePicker extends StatelessWidget {
  const _NodePicker({required this.nodes, required this.selected, required this.enabled, required this.onChanged});
  final List<Node> nodes;
  final String? selected;
  final bool enabled;
  final ValueChanged<String> onChanged;

  @override
  Widget build(BuildContext context) {
    return DropdownButtonFormField<String>(
      initialValue: nodes.any((n) => n.id == selected) ? selected : null,
      isExpanded: true,
      decoration: const InputDecoration(labelText: '节点', border: OutlineInputBorder()),
      items: [
        for (final n in nodes) DropdownMenuItem(value: n.id, child: Text('${n.name}  ·  ${n.region}')),
      ],
      onChanged: enabled ? (v) => v == null ? null : onChanged(v) : null,
    );
  }
}
