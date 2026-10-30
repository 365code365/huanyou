import 'dart:async';

import 'package:flutter/material.dart';

import '../../core/config.dart';
import '../../core/format.dart';
import '../../core/node_quality.dart';
import '../../core/vpn.dart';
import '../../state/app_controller.dart';
import '../../state/app_scope.dart';
import '../../theme/app_theme.dart';
import '../../widgets/ad_banner.dart';
import '../../widgets/brand_mark.dart';
import '../../widgets/connect_button.dart';
import '../../widgets/region_flag.dart';
import '../nodes/node_list_page.dart';
import '../plans/plans_page.dart';
import '../../i18n/i18n.dart';

/// Home tab: status headline, power button, current node, connection mode.
class HomePage extends StatefulWidget {
  const HomePage({super.key});

  @override
  State<HomePage> createState() => _HomePageState();
}

class _HomePageState extends State<HomePage> {
  Future<void> _handle(Future<ConnectResult?> f) async {
    final r = await f;
    if (r == ConnectResult.needPlan && mounted) {
      await openPlans(context);
    }
  }

  @override
  Widget build(BuildContext context) {
    final app = AppScope.of(context);
    return Scaffold(
      body: SafeArea(
        bottom: false,
        child: LayoutBuilder(builder: (context, c) {
          final btn = (c.maxHeight * 0.25).clamp(150.0, 196.0);
          return SingleChildScrollView(
            physics: const ClampingScrollPhysics(),
            child: ConstrainedBox(
              constraints: BoxConstraints(minHeight: c.maxHeight),
              child: Padding(
                padding: EdgeInsets.fromLTRB(22, 10, 22, MediaQuery.paddingOf(context).bottom + 16),
                child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
                  _TopBar(app: app),
                  SizedBox(height: c.maxHeight * 0.045),
                  _Headline(app: app),
                  SizedBox(height: c.maxHeight * 0.035),
                  Center(
                    child: ConnectButton(
                      key: const Key('connect-button'),
                      status: app.status,
                      size: btn,
                      // Also active while connecting: a second tap cancels.
                      onTap: app.status == VpnStatus.disconnecting ? null : () => _handle(app.toggle()),
                    ),
                  ),
                  if (app.message != null) ...[
                    const SizedBox(height: 6),
                    _Notice(text: app.message!),
                  ],
                  SizedBox(height: c.maxHeight * 0.035),
                  _NodeRow(
                    app: app,
                    onTap: () => app.nodes.isEmpty ? app.refresh() : _handle(pickNodeAndConnect(context)),
                  ),
                  const SizedBox(height: 12),
                  _ModeSegments(app: app, onChanged: (m) => _handle(app.setMode(m))),
                  // Admin-configured banner fills the space under the controls (off by default).
                  const SizedBox(height: 18),
                  const AdBanner(),
                ]),
              ),
            ),
          );
        }),
      ),
    );
  }
}

class _TopBar extends StatelessWidget {
  const _TopBar({required this.app});
  final AppController app;

  @override
  Widget build(BuildContext context) {
    final e = app.user?.entitlement;
    final String chip;
    if (e == null) {
      chip = app.loadFailed ? tr('重试') : tr('加载中');
    } else if (!e.canConnect) {
      chip = tr('去开通');
    } else if (e.paid && e.expiresAt != null) {
      final days = e.expiresAt!.difference(DateTime.now()).inDays;
      chip = tr('{0} · {1} 天', [e.planName, days < 0 ? 0 : days]);
    } else if (e.quotaBytes > 0) {
      chip = tr('免费 · 剩 {0}', [formatBytes((e.quotaBytes - e.usedBytes).clamp(0, e.quotaBytes))]);
    } else {
      chip = e.planName;
    }
    final warn = (e == null && app.loadFailed) || (e != null && (!e.canConnect || e.throttled));

    return Row(children: [
      const BrandMark(size: 30),
      const SizedBox(width: 10),
      Text(AppConfig.appName, style: const TextStyle(fontSize: 17, fontWeight: FontWeight.w700, letterSpacing: 0.2)),
      const Spacer(),
      Material(
        color: warn ? AppColors.danger.withValues(alpha: 0.09) : AppColors.sunken,
        borderRadius: BorderRadius.circular(20),
        child: InkWell(
          key: const Key('plan-chip'),
          borderRadius: BorderRadius.circular(20),
          onTap: () => e == null ? app.refresh() : openPlans(context),
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 7),
            child: Row(mainAxisSize: MainAxisSize.min, children: [
              if (e?.paid == true) ...[
                const Icon(Icons.workspace_premium_rounded, size: 15, color: AppColors.warning),
                const SizedBox(width: 4),
              ],
              Text(chip,
                  key: const Key('plan-name'),
                  style: TextStyle(
                      fontSize: 13, fontWeight: FontWeight.w600, color: warn ? AppColors.danger : AppColors.ink)),
              const SizedBox(width: 2),
              Icon(Icons.chevron_right_rounded, size: 16, color: warn ? AppColors.danger : AppColors.sub),
            ]),
          ),
        ),
      ),
    ]);
  }
}

/// "已连接 00:12:34": the only part of the home screen that changes every
/// second, so only this text rebuilds on the clock tick.
class _Elapsed extends StatefulWidget {
  const _Elapsed({required this.since, required this.style});
  final DateTime since;
  final TextStyle style;
  @override
  State<_Elapsed> createState() => _ElapsedState();
}

class _ElapsedState extends State<_Elapsed> {
  Timer? _t;

  @override
  void initState() {
    super.initState();
    _t = Timer.periodic(const Duration(seconds: 1), (_) {
      if (mounted) setState(() {});
    });
  }

  @override
  void dispose() {
    _t?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final d = DateTime.now().difference(widget.since);
    String two(int n) => n.toString().padLeft(2, '0');
    final hms = '${two(d.inHours)}:${two(d.inMinutes % 60)}:${two(d.inSeconds % 60)}';
    return Text(tr('已连接 {0}', [hms]), style: widget.style);
  }
}

class _Headline extends StatelessWidget {
  const _Headline({required this.app});
  final AppController app;
  @override
  Widget build(BuildContext context) {
    final (title, color) = switch (app.status) {
      VpnStatus.connected => (tr('已保护'), AppColors.signal),
      VpnStatus.connecting => (tr('连接中'), AppColors.ink),
      VpnStatus.disconnecting => (app.cancelling ? tr('正在取消') : tr('断开中'), AppColors.sub),
      VpnStatus.error => (tr('连接失败'), AppColors.danger),
      VpnStatus.disconnected => (tr('未连接'), AppColors.ink),
    };
    final subStyle = TextStyle(
      fontSize: 14,
      color: AppColors.sub,
      fontFeatures: [FontFeature.tabularFigures()],
    );
    final sub = switch (app.status) {
      VpnStatus.connected => '',
      VpnStatus.connecting => app.statusHint ?? tr('正在建立加密通道…'),
      VpnStatus.disconnecting => ' ',
      _ => tr('轻点下方按钮开始'),
    };
    return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
      AnimatedSwitcher(
        duration: const Duration(milliseconds: 220),
        child: Text(title,
            key: ValueKey(title),
            style: TextStyle(fontSize: 34, height: 1.1, fontWeight: FontWeight.w800, color: color, letterSpacing: -0.5)),
      ),
      const SizedBox(height: 6),
      if (app.status == VpnStatus.connected && app.connectedAt != null)
        _Elapsed(since: app.connectedAt!, style: subStyle)
      else if (app.canCancel)
        Text.rich(
            TextSpan(children: [
              TextSpan(text: sub),
              const TextSpan(text: '  ·  '),
              TextSpan(text: tr('再次点按可取消'), style: subStyle.copyWith(color: AppColors.faint)),
            ]),
            style: subStyle)
      else
        Text(sub, style: subStyle),
    ]);
  }
}

class _Notice extends StatelessWidget {
  const _Notice({required this.text});
  final String text;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 10),
      decoration: BoxDecoration(
        color: AppColors.danger.withValues(alpha: 0.07),
        borderRadius: BorderRadius.circular(12),
      ),
      child: Row(children: [
        const Icon(Icons.info_outline_rounded, size: 17, color: AppColors.danger),
        const SizedBox(width: 8),
        Expanded(
          child: Text(text,
              key: const Key('home-message'), style: const TextStyle(color: AppColors.danger, fontSize: 13)),
        ),
      ]),
    );
  }
}

class _NodeRow extends StatelessWidget {
  const _NodeRow({required this.app, required this.onTap});
  final AppController app;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    // Cluster and AI 优先: the server picks the nodes.
    final auto = app.mode == ConnectMode.cluster || app.mode == ConnectMode.ai;
    final cluster = auto;
    final n = app.selectedNode;
    return Card(
      clipBehavior: Clip.antiAlias,
      child: InkWell(
        key: const Key('node-card'),
        onTap: app.status == VpnStatus.disconnecting ? null : onTap,
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 14),
          child: Row(children: [
            if (cluster)
              Container(
                width: 40,
                height: 40,
                decoration: BoxDecoration(color: AppColors.sunken, shape: BoxShape.circle),
                child: Icon(app.mode == ConnectMode.ai ? Icons.auto_awesome_rounded : Icons.hub_rounded, color: AppColors.ink, size: 20),
              )
            else
              RegionFlag(n?.region ?? '', size: 40),
            const SizedBox(width: 14),
            Expanded(
              child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                Text(nodeLabel(app),
                    key: const Key('node-card-name'),
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: const TextStyle(fontSize: 15, fontWeight: FontWeight.w600)),
                const SizedBox(height: 2),
                Text(
                    app.mode == ConnectMode.ai
                        ? tr('自动选择最稳定的节点，断线自动切换')
                        : cluster
                            ? tr('{0} 个节点自动切换', [app.nodes.where((x) => x.online).length])
                            : tr('轻点切换节点'),
                    style: TextStyle(fontSize: 13, color: AppColors.sub)),
              ]),
            ),
            if (!cluster && n != null) _QualityBadge(score: app.scoreOf(n)),
            const SizedBox(width: 4),
            Icon(Icons.chevron_right_rounded, color: AppColors.faint),
          ]),
        ),
      ),
    );
  }
}

class _QualityBadge extends StatelessWidget {
  const _QualityBadge({required this.score});
  final NodeScore score;

  @override
  Widget build(BuildContext context) {
    final level = score.level;
    final color = switch (level) {
      QualityLevel.excellent || QualityLevel.good => AppColors.signal,
      QualityLevel.fair => AppColors.warning,
      QualityLevel.poor || QualityLevel.down => AppColors.danger,
      QualityLevel.unknown => AppColors.sub,
    };
    final ms = score.quality?.latencyMs;
    return Row(mainAxisSize: MainAxisSize.min, children: [
      Container(width: 7, height: 7, decoration: BoxDecoration(color: color, shape: BoxShape.circle)),
      const SizedBox(width: 5),
      Text(ms != null ? '${level.label} ${ms}ms' : (score.node.online ? tr('在线') : tr('维护中')),
          style: TextStyle(fontSize: 12, color: color, fontWeight: FontWeight.w600)),
    ]);
  }
}

/// iOS-style segmented control for the connection mode.
class _ModeSegments extends StatelessWidget {
  const _ModeSegments({required this.app, required this.onChanged});
  final AppController app;
  final ValueChanged<ConnectMode> onChanged;

  @override
  Widget build(BuildContext context) {
    const modes = ConnectMode.values;
    final idx = modes.indexOf(app.mode);
    return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
      Container(
        height: 46,
        padding: const EdgeInsets.all(4),
        decoration: BoxDecoration(color: AppColors.sunken, borderRadius: BorderRadius.circular(14)),
        child: LayoutBuilder(builder: (context, c) {
          final w = c.maxWidth / modes.length;
          return Stack(children: [
            AnimatedPositioned(
              duration: const Duration(milliseconds: 220),
              curve: Curves.easeOutCubic,
              left: w * idx,
              top: 0,
              bottom: 0,
              width: w,
              child: Container(
                decoration: BoxDecoration(
                  color: AppColors.surface,
                  borderRadius: BorderRadius.circular(10),
                  boxShadow: [
                    BoxShadow(color: AppColors.shadow.withValues(alpha: 0.08), blurRadius: 6, offset: const Offset(0, 2)),
                  ],
                ),
              ),
            ),
            Row(children: [
              for (final m in modes)
                Expanded(
                  child: InkWell(
                    key: Key('mode-${m.id}'),
                    borderRadius: BorderRadius.circular(10),
                    onTap: app.status == VpnStatus.disconnecting ? null : () => onChanged(m),
                    child: Center(
                      child: Text(m.label,
                          style: TextStyle(
                            fontSize: 14,
                            fontWeight: m == app.mode ? FontWeight.w700 : FontWeight.w500,
                            color: m == app.mode ? AppColors.ink : AppColors.sub,
                          )),
                    ),
                  ),
                ),
            ]),
          ]);
        }),
      ),
      const SizedBox(height: 8),
      Padding(
        padding: const EdgeInsets.only(left: 4),
        child: AnimatedSwitcher(
          duration: const Duration(milliseconds: 180),
          child: Text(app.mode.hint,
              key: ValueKey(app.mode), style: TextStyle(fontSize: 13, color: AppColors.sub)),
        ),
      ),
    ]);
  }
}
