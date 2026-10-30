import 'package:flutter/material.dart';

import '../../core/api.dart';
import '../../core/node_quality.dart';
import '../../state/app_controller.dart';
import '../../state/app_scope.dart';
import '../../theme/app_theme.dart';
import '../../widgets/region_flag.dart';
import '../../i18n/i18n.dart';

/// What the user picked on the node page.
sealed class NodeChoice {
  const NodeChoice();
}

/// Let the cluster balancer pick (switches to cluster mode).
class AutoNodeChoice extends NodeChoice {
  const AutoNodeChoice();
}

class SpecificNodeChoice extends NodeChoice {
  const SpecificNodeChoice(this.nodeId);
  final String nodeId;
}

enum _Sort { quality, latency, region }

/// Full-screen node picker with live quality checks. Opening the page measures
/// every node from this device; nodes are ranked by a combined score
/// (latency, packet loss, 1-hour uptime, current load). Pops with a [NodeChoice].
class NodeListPage extends StatefulWidget {
  const NodeListPage({super.key});

  @override
  State<NodeListPage> createState() => _NodeListPageState();
}

class _NodeListPageState extends State<NodeListPage> {
  _Sort _sort = _Sort.quality;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => AppScope.read(context).probeNodes());
  }

  Future<void> _refresh() async {
    final app = AppScope.read(context);
    await app.refresh(quiet: true);
    await app.probeNodes(force: true);
  }

  List<NodeScore> _ordered(AppController app) {
    final list = app.rankedNodes; // already best-first
    switch (_sort) {
      case _Sort.quality:
        return list;
      case _Sort.latency:
        int lat(NodeScore s) => s.quality?.latencyMs ?? 1 << 30;
        return list..sort((a, b) => lat(a).compareTo(lat(b)));
      case _Sort.region:
        return list..sort((a, b) => '${a.node.region}${a.node.name}'.compareTo('${b.node.region}${b.node.name}'));
    }
  }

  @override
  Widget build(BuildContext context) {
    final app = AppScope.of(context);
    final cluster = app.mode == ConnectMode.cluster;
    final best = app.bestNode;
    final ordered = _ordered(app);
    final usable = ordered.where((s) => s.level != QualityLevel.down).length;

    return Scaffold(
      appBar: AppBar(
        title: Text(tr('选择节点')),
        actions: [
          TextButton.icon(
            key: const Key('probe-nodes'),
            onPressed: app.probing ? null : () => app.probeNodes(force: true),
            icon: app.probing
                ? const SizedBox.square(dimension: 15, child: CircularProgressIndicator(strokeWidth: 2))
                : const Icon(Icons.speed_rounded, size: 19),
            label: Text(app.probing ? tr('测速中') : tr('测速')),
          ),
          const SizedBox(width: 6),
        ],
      ),
      body: RefreshIndicator(
        color: AppColors.ink,
        onRefresh: _refresh,
        child: ListView(
          padding: const EdgeInsets.fromLTRB(16, 4, 16, 32),
          children: [
            _NodeTile(
              key: const Key('node-auto'),
              leading: Container(
                width: 40,
                height: 40,
                decoration: BoxDecoration(color: AppColors.sunken, shape: BoxShape.circle),
                child: Icon(Icons.hub_rounded, color: AppColors.ink, size: 20),
              ),
              title: tr('智能选择'),
              subtitle: tr('集群模式 · 自动连接最优节点，断线自动切换（{0}/{1} 可用）', [usable, app.nodes.length]),
              selected: cluster,
              onTap: () => Navigator.pop(context, const AutoNodeChoice()),
            ),
            if (best != null && app.nodes.length > 1) ...[
              const SizedBox(height: 10),
              _BestBanner(
                score: best,
                current: !cluster && best.node.id == app.selectedNode?.id,
                onTap: () => Navigator.pop(context, SpecificNodeChoice(best.node.id)),
              ),
            ],
            Padding(
              padding: const EdgeInsets.fromLTRB(4, 20, 0, 10),
              child: Row(children: [
                Text(tr('全部节点'), style: TextStyle(color: AppColors.sub, fontSize: 13, fontWeight: FontWeight.w600)),
                const Spacer(),
                _SortChips(value: _sort, onChanged: (s) => setState(() => _sort = s)),
              ]),
            ),
            if (app.nodes.isEmpty)
              Padding(
                padding: EdgeInsets.symmetric(vertical: 60),
                child: Center(child: Text(tr('暂无可用节点'), style: TextStyle(color: AppColors.sub))),
              ),
            for (final s in ordered)
              Padding(
                padding: const EdgeInsets.only(bottom: 10),
                child: _NodeTile(
                  key: Key('node-${s.node.id}'),
                  leading: RegionFlag(s.node.region),
                  title: s.node.name,
                  badge: best != null && s.node.id == best.node.id && app.nodes.length > 1 ? tr('推荐') : null,
                  subtitle: _detail(s, app.probing),
                  trailing: _QualityMeter(level: s.level, measuring: app.probing && s.quality == null),
                  selected: !cluster && s.node.id == app.selectedNode?.id,
                  enabled: s.level != QualityLevel.down,
                  onTap: () => Navigator.pop(context, SpecificNodeChoice(s.node.id)),
                ),
              ),
            if (app.nodes.isNotEmpty)
              Padding(
                padding: EdgeInsets.fromLTRB(6, 6, 6, 0),
                child: Text(
                  tr('质量综合了本机实测延迟、丢包、近 1 小时稳定性和当前负载。网络环境变化后可点右上角重新测速。'),
                  style: TextStyle(color: AppColors.faint, fontSize: 12, height: 1.6),
                ),
              ),
          ],
        ),
      ),
    );
  }

  String _detail(NodeScore s, bool probing) {
    if (!s.node.online) return tr('节点维护中');
    final q = s.quality;
    final parts = <String>[];
    if (q == null) {
      parts.add(probing ? tr('测速中…') : tr('未测速'));
    } else if (!q.reachable) {
      return tr('当前网络无法连通');
    } else {
      parts.add('${q.latencyMs}ms');
      if (q.loss > 0) parts.add(tr('丢包 {0}%', [(q.loss * 100).round()]));
    }
    parts.add(tr('稳定 {0}%', [(s.node.uptime * 100).round()]));
    parts.add(tr('负载{0}', [s.loadLabel]));
    return parts.join(' · ');
  }
}

class _BestBanner extends StatelessWidget {
  const _BestBanner({required this.score, required this.current, required this.onTap});
  final NodeScore score;
  final bool current;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    return Material(
      color: AppColors.signal.withValues(alpha: 0.08),
      borderRadius: BorderRadius.circular(16),
      child: InkWell(
        key: const Key('node-best'),
        borderRadius: BorderRadius.circular(16),
        onTap: current ? null : onTap,
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 12),
          child: Row(children: [
            const Icon(Icons.bolt_rounded, color: AppColors.signal, size: 22),
            const SizedBox(width: 10),
            Expanded(
              child: Text.rich(
                TextSpan(children: [
                  TextSpan(text: tr('当前最优：')),
                  TextSpan(text: '${flagEmoji(score.node.region)} ${score.node.name}', style: const TextStyle(fontWeight: FontWeight.w700)),
                  TextSpan(text: '  ${score.quality!.latencyMs}ms'),
                ]),
                style: TextStyle(fontSize: 13.5, color: AppColors.ink),
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
              ),
            ),
            Text(current ? tr('使用中') : tr('切换'),
                style: const TextStyle(fontSize: 13, color: AppColors.signal, fontWeight: FontWeight.w700)),
          ]),
        ),
      ),
    );
  }
}

class _SortChips extends StatelessWidget {
  const _SortChips({required this.value, required this.onChanged});
  final _Sort value;
  final ValueChanged<_Sort> onChanged;

  @override
  Widget build(BuildContext context) {
    final labels = {_Sort.quality: tr('推荐'), _Sort.latency: tr('延迟'), _Sort.region: tr('地区')};
    return Container(
      padding: const EdgeInsets.all(3),
      decoration: BoxDecoration(color: AppColors.sunken, borderRadius: BorderRadius.circular(10)),
      child: Row(mainAxisSize: MainAxisSize.min, children: [
        for (final e in labels.entries)
          GestureDetector(
            onTap: () => onChanged(e.key),
            child: AnimatedContainer(
              duration: const Duration(milliseconds: 160),
              padding: const EdgeInsets.symmetric(horizontal: 11, vertical: 5),
              decoration: BoxDecoration(
                color: value == e.key ? AppColors.surface : Colors.transparent,
                borderRadius: BorderRadius.circular(8),
              ),
              child: Text(e.value,
                  style: TextStyle(
                    fontSize: 12.5,
                    fontWeight: value == e.key ? FontWeight.w700 : FontWeight.w500,
                    color: value == e.key ? AppColors.ink : AppColors.sub,
                  )),
            ),
          ),
      ]),
    );
  }
}

/// Four signal bars plus the level label.
class _QualityMeter extends StatelessWidget {
  const _QualityMeter({required this.level, required this.measuring});
  final QualityLevel level;
  final bool measuring;

  @override
  Widget build(BuildContext context) {
    final color = switch (level) {
      QualityLevel.excellent || QualityLevel.good => AppColors.signal,
      QualityLevel.fair => AppColors.warning,
      QualityLevel.poor || QualityLevel.down => AppColors.danger,
      QualityLevel.unknown => AppColors.faint,
    };
    return Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.end, children: [
      Row(crossAxisAlignment: CrossAxisAlignment.end, mainAxisSize: MainAxisSize.min, children: [
        for (var i = 0; i < 4; i++)
          AnimatedContainer(
            duration: const Duration(milliseconds: 250),
            width: 4,
            height: 6.0 + i * 3.5,
            margin: const EdgeInsets.only(left: 2.5),
            decoration: BoxDecoration(
              color: i < level.bars ? color : AppColors.line,
              borderRadius: BorderRadius.circular(1.5),
            ),
          ),
      ]),
      const SizedBox(height: 4),
      Text(measuring ? '…' : level.label, style: TextStyle(fontSize: 11, color: color, fontWeight: FontWeight.w600)),
    ]);
  }
}

class _NodeTile extends StatelessWidget {
  const _NodeTile({
    super.key,
    required this.leading,
    required this.title,
    required this.subtitle,
    required this.selected,
    required this.onTap,
    this.trailing,
    this.badge,
    this.enabled = true,
  });

  final Widget leading;
  final String title;
  final String subtitle;
  final Widget? trailing;
  final String? badge;
  final bool selected;
  final bool enabled;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    return Opacity(
      opacity: enabled ? 1 : 0.5,
      child: AnimatedContainer(
        duration: const Duration(milliseconds: 180),
        decoration: BoxDecoration(
          color: AppColors.surface,
          borderRadius: BorderRadius.circular(18),
          border: Border.all(color: selected ? AppColors.ink : AppColors.line, width: selected ? 1.6 : 1),
        ),
        child: Material(
          color: Colors.transparent,
          child: InkWell(
            borderRadius: BorderRadius.circular(18),
            onTap: enabled ? onTap : null,
            child: Padding(
              padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 14),
              child: Row(children: [
                leading,
                const SizedBox(width: 14),
                Expanded(
                  child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                    Row(children: [
                      Flexible(
                        child: Text(title,
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: const TextStyle(fontSize: 16, fontWeight: FontWeight.w600)),
                      ),
                      if (badge != null) ...[
                        const SizedBox(width: 6),
                        Container(
                          padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1.5),
                          decoration: BoxDecoration(color: AppColors.signal, borderRadius: BorderRadius.circular(6)),
                          child: Text(badge!,
                              style: const TextStyle(fontSize: 10.5, color: Colors.white, fontWeight: FontWeight.w700)),
                        ),
                      ],
                    ]),
                    const SizedBox(height: 3),
                    Text(subtitle,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: TextStyle(fontSize: 12, color: AppColors.sub)),
                  ]),
                ),
                if (trailing != null) ...[const SizedBox(width: 8), trailing!, const SizedBox(width: 10)],
                Icon(
                  selected ? Icons.check_circle_rounded : Icons.circle_outlined,
                  color: selected ? AppColors.ink : AppColors.line,
                  size: 22,
                ),
              ]),
            ),
          ),
        ),
      ),
    );
  }
}

/// Opens the node page and applies the choice (auto-connects). Returns the connect result.
Future<ConnectResult?> pickNodeAndConnect(BuildContext context) async {
  final app = AppScope.read(context);
  final choice = await Navigator.push<NodeChoice>(
    context,
    MaterialPageRoute(builder: (_) => const NodeListPage()),
  );
  switch (choice) {
    case null:
      return null;
    case AutoNodeChoice():
      if (app.mode != ConnectMode.cluster) {
        final r = await app.setMode(ConnectMode.cluster);
        if (r != null) return r; // was connected → reconnected
      }
      return app.active ? ConnectResult.ok : app.connect();
    case SpecificNodeChoice(:final nodeId):
      return app.selectNode(nodeId);
  }
}

/// Kept for callers that only need a label for the current selection.
String nodeLabel(AppController app) {
  if (app.mode == ConnectMode.cluster) return tr('智能选择');
  final Node? n = app.selectedNode;
  if (n != null) return n.name;
  return app.loadFailed ? tr('加载失败，点击重试') : (app.user == null ? tr('加载中…') : tr('暂无节点'));
}
