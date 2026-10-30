import 'dart:math';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/api.dart';
import '../../core/format.dart';
import '../../state/app_scope.dart';
import '../../theme/app_theme.dart';
import '../../widgets/section_card.dart';
import '../devices/devices_page.dart';
import '../plans/plans_page.dart';
import '../referral/referral_page.dart';
import 'contacts_section.dart';
import 'orders_page.dart';

/// "我的" tab: membership card (recharge ID, plan, traffic), usage, links.
class MePage extends StatefulWidget {
  const MePage({super.key});

  @override
  State<MePage> createState() => _MePageState();
}

class _MePageState extends State<MePage> {
  List<DailyUsage> _usage = [];
  List<Contact> _contacts = [];

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    final app = AppScope.read(context);
    await app.refreshUser();
    await Future.wait([
      app.api.usage().then((u) {
        if (mounted) setState(() => _usage = u);
      }).catchError((Object _) {}), // keep the previous chart
      app.api.contacts().then((c) {
        if (mounted) setState(() => _contacts = c);
      }).catchError((Object _) {}),
    ]);
  }

  Future<void> _logout() async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('退出邮箱账号'),
        content: const Text('退出后本机会回到设备账号，VPN 将断开。'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('取消')),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('退出')),
        ],
      ),
    );
    if (ok == true && mounted) await AppScope.read(context).logout();
  }

  @override
  Widget build(BuildContext context) {
    final app = AppScope.of(context);
    final u = app.user;
    return Scaffold(
      appBar: AppBar(title: const Text('我的')),
      body: RefreshIndicator(
        color: AppColors.ink,
        onRefresh: _load,
        child: ListView(
          padding: EdgeInsets.fromLTRB(18, 4, 18, MediaQuery.paddingOf(context).bottom + 24),
          children: [
            if (u != null) _MemberCard(user: u, onUpgrade: () => openPlans(context)),
            const SizedBox(height: 14),
            SectionCard(title: '近 30 天流量', child: _UsageChart(days: _usage)),
            const SizedBox(height: 14),
            Card(
              clipBehavior: Clip.antiAlias,
              child: Column(children: [
                MenuRow(
                  icon: Icons.workspace_premium_outlined,
                  color: AppColors.warning,
                  title: '购买套餐',
                  onTap: () => openPlans(context),
                ),
                const Divider(indent: 66),
                MenuRow(
                  icon: Icons.card_giftcard_rounded,
                  color: AppColors.violet,
                  title: '邀请好友送时长',
                  onTap: () => Navigator.push(context, MaterialPageRoute(builder: (_) => const ReferralPage())),
                ),
                const Divider(indent: 66),
                MenuRow(
                  key: const Key('menu-devices'),
                  icon: Icons.devices_rounded,
                  title: '多设备',
                  value: u != null && u.linked ? '使用 ${u.ownerCode}' : '扫码 / 令牌',
                  onTap: () async {
                    await Navigator.push(context, MaterialPageRoute(builder: (_) => const DevicesPage()));
                    _load();
                  },
                ),
                const Divider(indent: 66),
                MenuRow(
                  icon: Icons.receipt_long_outlined,
                  title: '购买记录',
                  onTap: () => Navigator.push(context, MaterialPageRoute(builder: (_) => const OrdersPage())),
                ),
              ]),
            ),
            ContactsSection(contacts: _contacts),
            const SizedBox(height: 18),
            const Padding(
              padding: EdgeInsets.symmetric(horizontal: 6),
              child: Text(
                '本机已自动开通账号，卸载重装后会自动找回。需要充值时，把上方的充值 ID 发给客服即可。',
                style: TextStyle(fontSize: 12.5, color: AppColors.sub, height: 1.5),
              ),
            ),
            if (u != null && u.hasAccount) ...[
              const SizedBox(height: 14),
              Card(
                clipBehavior: Clip.antiAlias,
                child: MenuRow(key: const Key('logout'), icon: Icons.logout_rounded, title: '退出邮箱账号', danger: true, onTap: _logout),
              ),
            ],
          ],
        ),
      ),
    );
  }
}

/// Dark membership card. The recharge ID is the user's only identity: large,
/// copyable, and the thing support asks for when topping up.
class _MemberCard extends StatelessWidget {
  const _MemberCard({required this.user, required this.onUpgrade});
  final User user;
  final VoidCallback onUpgrade;

  @override
  Widget build(BuildContext context) {
    final e = user.entitlement;
    final warn = e.throttled || !e.canConnect;
    final code = user.rechargeCode;
    const dim = Color(0xFF9A9CA3);
    return Container(
      decoration: BoxDecoration(color: AppColors.ink, borderRadius: BorderRadius.circular(AppRadius.card + 2)),
      padding: const EdgeInsets.fromLTRB(20, 18, 14, 20),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Row(children: [
          const Text('充值 ID', style: TextStyle(color: dim, fontSize: 12.5)),
          const Spacer(),
          if (user.hasAccount)
            Text(user.email, style: const TextStyle(color: dim, fontSize: 12), overflow: TextOverflow.ellipsis),
        ]),
        const SizedBox(height: 4),
        Row(children: [
          Text(code.isEmpty ? '—' : code,
              key: const Key('recharge-code'),
              style: const TextStyle(
                color: Colors.white,
                fontSize: 28,
                fontWeight: FontWeight.w800,
                letterSpacing: 1,
                fontFeatures: [FontFeature.tabularFigures()],
              )),
          const SizedBox(width: 6),
          IconButton(
            tooltip: '复制',
            visualDensity: VisualDensity.compact,
            onPressed: code.isEmpty
                ? null
                : () {
                    Clipboard.setData(ClipboardData(text: code));
                    ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('充值 ID 已复制')));
                  },
            icon: const Icon(Icons.copy_rounded, size: 18, color: dim),
          ),
        ]),
        const SizedBox(height: 16),
        Row(crossAxisAlignment: CrossAxisAlignment.end, children: [
          Expanded(
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text(e.planName.isEmpty ? '无套餐' : e.planName,
                  key: const Key('me-plan'),
                  style: const TextStyle(color: Colors.white, fontSize: 16, fontWeight: FontWeight.w700)),
              const SizedBox(height: 2),
              Text(
                user.linked
                    ? '共享 ${user.ownerCode} 的订阅${e.paid && e.expiresAt != null ? ' · ${formatDate(e.expiresAt!)} 到期' : ''}'
                    : e.paid && e.expiresAt != null ? '${formatDate(e.expiresAt!)} 到期' : '免费体验中',
                style: const TextStyle(color: dim, fontSize: 12.5),
              ),
            ]),
          ),
          Padding(
            padding: const EdgeInsets.only(right: 6),
            child: FilledButton(
              style: FilledButton.styleFrom(
                backgroundColor: Colors.white,
                foregroundColor: AppColors.ink,
                visualDensity: VisualDensity.compact,
                padding: const EdgeInsets.symmetric(horizontal: 18),
                shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
              ),
              onPressed: user.linked ? null : onUpgrade,
              child: Text(user.linked ? '共享中' : e.paid ? '续费' : '开通会员', style: const TextStyle(fontWeight: FontWeight.w700)),
            ),
          ),
        ]),
        const SizedBox(height: 16),
        Padding(
          padding: const EdgeInsets.only(right: 6),
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Row(children: [
              Text(
                e.quotaBytes > 0
                    ? '${formatBytes(e.usedBytes)} / ${formatBytes(e.quotaBytes)}'
                    : '已用 ${formatBytes(e.usedBytes)} · 不限流量',
                key: const Key('me-usage'),
                style: const TextStyle(color: Colors.white, fontSize: 13, fontFeatures: [FontFeature.tabularFigures()]),
              ),
              const Spacer(),
              if (e.periodEnd != null && e.quotaBytes > 0)
                Text('${formatDate(e.periodEnd!)} 重置', style: const TextStyle(color: dim, fontSize: 12)),
            ]),
            if (e.quotaBytes > 0) ...[
              const SizedBox(height: 8),
              ClipRRect(
                borderRadius: BorderRadius.circular(3),
                child: LinearProgressIndicator(
                  value: e.usedFraction,
                  minHeight: 5,
                  backgroundColor: Colors.white.withValues(alpha: 0.12),
                  color: warn ? const Color(0xFFFF8A80) : AppColors.signal,
                ),
              ),
            ],
            if (warn) ...[
              const SizedBox(height: 10),
              Text(
                !e.canConnect ? (e.reason ?? '暂时无法连接') : '流量已用完，当前限速 ${formatSpeed(e.speedKbps)}',
                style: const TextStyle(color: Color(0xFFFFB3AB), fontSize: 12),
              ),
            ],
          ]),
        ),
      ]),
    );
  }
}

class _UsageChart extends StatelessWidget {
  const _UsageChart({required this.days});
  final List<DailyUsage> days;

  @override
  Widget build(BuildContext context) {
    final byDay = {for (final d in days) d.day: d.total};
    final today = DateTime.now();
    final series = [for (var i = 29; i >= 0; i--) byDay[formatDate(today.subtract(Duration(days: i)))] ?? 0];
    final maxV = series.fold<int>(0, max);
    final total = series.fold<int>(0, (a, b) => a + b);
    return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
      SizedBox(
        height: 64,
        child: Row(crossAxisAlignment: CrossAxisAlignment.end, children: [
          for (var i = 0; i < series.length; i++)
            Expanded(
              child: Container(
                margin: const EdgeInsets.symmetric(horizontal: 1.3),
                height: maxV == 0 ? 3 : max(3, 64 * series[i] / maxV),
                decoration: BoxDecoration(
                  color: series[i] == 0 ? AppColors.sunken : (i == series.length - 1 ? AppColors.signal : AppColors.ink),
                  borderRadius: BorderRadius.circular(2),
                ),
              ),
            ),
        ]),
      ),
      const SizedBox(height: 10),
      Text('合计 ${formatBytes(total)} · 单日最高 ${formatBytes(maxV)}',
          key: const Key('usage-total'), style: const TextStyle(color: AppColors.sub, fontSize: 12)),
    ]);
  }
}
