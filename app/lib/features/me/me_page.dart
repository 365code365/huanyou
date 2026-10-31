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
import '../../core/updater.dart';
import 'contacts_section.dart';
import 'orders_page.dart';
import 'settings_page.dart';
import '../../i18n/i18n.dart';

/// "我的" tab: membership card (recharge ID, plan, traffic), usage, links.
class MePage extends StatefulWidget {
  const MePage({super.key});

  @override
  State<MePage> createState() => _MePageState();
}

class _MePageState extends State<MePage> {
  List<DailyUsage> _usage = [];
  List<Contact> _contacts = [];
  String _version = '';

  @override
  void initState() {
    super.initState();
    _load();
    Updater.info().then((i) {
      if (mounted) setState(() => _version = '${i.version} (${Updater.buildOf(i)})');
    });
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
        title: Text(tr('退出邮箱账号')),
        content: Text(tr('退出后本机会回到设备账号，VPN 将断开。')),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: Text(tr('取消'))),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: Text(tr('退出'))),
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
      appBar: AppBar(title: Text(tr('我的'))),
      body: RefreshIndicator(
        color: AppColors.ink,
        onRefresh: _load,
        child: ListView(
          padding: EdgeInsets.fromLTRB(18, 4, 18, MediaQuery.paddingOf(context).bottom + 24),
          children: [
            if (u != null)
              _MemberCard(user: u, store: app.iapFace, onUpgrade: () => openPlans(context))
            else if (app.loadFailed)
              Card(
                child: ListTile(
                  leading: Icon(Icons.cloud_off_rounded, color: AppColors.danger),
                  title: Text(tr('账号信息加载失败')),
                  subtitle: Text(app.message ?? tr('请检查网络后重试')),
                  trailing: TextButton(onPressed: app.refresh, child: Text(tr('重试'))),
                ),
              ),
            const SizedBox(height: 14),
            SectionCard(title: tr('近 30 天流量'), child: _UsageChart(days: _usage)),
            const SizedBox(height: 14),
            Card(
              clipBehavior: Clip.antiAlias,
              child: Column(children: [
                MenuRow(
                  icon: Icons.workspace_premium_outlined,
                  color: AppColors.warning,
                  title: tr('购买套餐'),
                  onTap: () => openPlans(context),
                ),
                const Divider(indent: 66),
                MenuRow(
                  icon: Icons.card_giftcard_rounded,
                  color: AppColors.violet,
                  title: tr('邀请好友送时长'),
                  onTap: () => Navigator.push(context, MaterialPageRoute(builder: (_) => const ReferralPage())),
                ),
                const Divider(indent: 66),
                MenuRow(
                  key: const Key('menu-devices'),
                  icon: Icons.devices_rounded,
                  title: tr('多设备'),
                  value: u != null && u.linked ? tr('使用 {0}', [u.ownerCode]) : tr('扫码 / 令牌'),
                  onTap: () async {
                    await Navigator.push(context, MaterialPageRoute(builder: (_) => const DevicesPage()));
                    _load();
                  },
                ),
                const Divider(indent: 66),
                MenuRow(
                  icon: Icons.receipt_long_outlined,
                  title: tr('购买记录'),
                  onTap: () => Navigator.push(context, MaterialPageRoute(builder: (_) => const OrdersPage())),
                ),
                const Divider(indent: 66),
                MenuRow(
                  key: const Key('menu-settings'),
                  icon: Icons.settings_outlined,
                  title: tr('设置'),
                  value: _version.isEmpty ? null : 'v$_version',
                  onTap: () => Navigator.push(context, MaterialPageRoute(builder: (_) => const SettingsPage())),
                ),
              ]),
            ),
            ContactsSection(contacts: _contacts),
            const SizedBox(height: 18),
            Padding(
              padding: EdgeInsets.symmetric(horizontal: 6),
              child: Text(
                // App Store face: no pointing to purchases outside the App Store.
                app.iapFace
                    ? tr('本机已自动开通账号，卸载重装后会自动找回。')
                    : tr('本机已自动开通账号，卸载重装后会自动找回。需要充值时，把上方的充值 ID 发给客服即可。'),
                style: TextStyle(fontSize: 13, color: AppColors.sub, height: 1.5),
              ),
            ),
            if (u != null && u.hasAccount) ...[
              const SizedBox(height: 14),
              Card(
                clipBehavior: Clip.antiAlias,
                child: MenuRow(key: const Key('logout'), icon: Icons.logout_rounded, title: tr('退出邮箱账号'), danger: true, onTap: _logout),
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
  const _MemberCard({required this.user, required this.store, required this.onUpgrade});
  final User user;
  final bool store; // App Store face: "账号 ID", not "充值 ID"
  final VoidCallback onUpgrade;

  @override
  Widget build(BuildContext context) {
    final e = user.entitlement;
    final warn = e.throttled || !e.canConnect;
    final code = user.rechargeCode;
    const dim = Color(0xFF9A9CA3);
    return Container(
      decoration: BoxDecoration(color: AppColors.hero, borderRadius: BorderRadius.circular(AppRadius.card + 2)),
      padding: const EdgeInsets.fromLTRB(20, 18, 14, 20),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Row(children: [
          Text(store ? tr('账号 ID') : tr('充值 ID'), style: TextStyle(color: dim, fontSize: 13)),
          const SizedBox(width: 12),
          const Spacer(),
          if (user.hasAccount)
            Flexible(
              flex: 4,
              child: Text(user.email,
                  maxLines: 1, style: const TextStyle(color: dim, fontSize: 12), overflow: TextOverflow.ellipsis),
            ),
        ]),
        const SizedBox(height: 4),
        Row(children: [
          Flexible(
            child: FittedBox(
              fit: BoxFit.scaleDown,
              alignment: Alignment.centerLeft,
              child: Text(code.isEmpty ? '—' : code,
                  key: const Key('recharge-code'),
                  style: const TextStyle(
                    color: Colors.white,
                    fontSize: 28,
                    fontWeight: FontWeight.w800,
                    letterSpacing: 1,
                    fontFeatures: [FontFeature.tabularFigures()],
                  )),
            ),
          ),
          const SizedBox(width: 6),
          IconButton(
            tooltip: tr('复制'),
            visualDensity: VisualDensity.compact,
            onPressed: code.isEmpty
                ? null
                : () {
                    Clipboard.setData(ClipboardData(text: code));
                    ScaffoldMessenger.of(context)
                        .showSnackBar(SnackBar(content: Text(store ? tr('账号 ID 已复制') : tr('充值 ID 已复制'))));
                  },
            icon: const Icon(Icons.copy_rounded, size: 18, color: dim),
          ),
        ]),
        const SizedBox(height: 16),
        Row(crossAxisAlignment: CrossAxisAlignment.end, children: [
          Expanded(
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text(e.planName.isEmpty ? tr('无套餐') : e.planName,
                  key: const Key('me-plan'),
                  style: const TextStyle(color: Colors.white, fontSize: 15, fontWeight: FontWeight.w700)),
              const SizedBox(height: 2),
              Text(
                user.linked
                    ? tr('共享 {0} 的订阅{1}', [user.ownerCode, e.paid && e.expiresAt != null ? tr(' · {0} 到期', [formatDate(e.expiresAt!)]) : ''])
                    : e.paid && e.expiresAt != null ? tr('{0} 到期', [formatDate(e.expiresAt!)]) : tr('免费体验中'),
                style: const TextStyle(color: dim, fontSize: 13),
              ),
            ]),
          ),
          Padding(
            padding: const EdgeInsets.only(right: 6),
            child: FilledButton(
              style: FilledButton.styleFrom(
                backgroundColor: Colors.white,
                foregroundColor: AppColors.night,
                visualDensity: VisualDensity.compact,
                padding: const EdgeInsets.symmetric(horizontal: 18),
                shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
              ),
              onPressed: user.linked ? null : onUpgrade,
              child: Text(user.linked ? tr('共享中') : e.paid ? tr('续费') : tr('开通会员'), style: const TextStyle(fontWeight: FontWeight.w700)),
            ),
          ),
        ]),
        const SizedBox(height: 16),
        Padding(
          padding: const EdgeInsets.only(right: 6),
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Row(children: [
              Expanded(
                child: Text(
                  e.quotaBytes > 0
                      ? '${formatBytes(e.usedBytes)} / ${formatBytes(e.quotaBytes)}'
                      : tr('已用 {0} · 不限流量', [formatBytes(e.usedBytes)]),
                  key: const Key('me-usage'),
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: const TextStyle(color: Colors.white, fontSize: 13, fontFeatures: [FontFeature.tabularFigures()]),
                ),
              ),
              if (e.periodEnd != null && e.quotaBytes > 0)
                Text(tr('{0} 重置', [formatDate(e.periodEnd!)]), style: const TextStyle(color: dim, fontSize: 12)),
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
                !e.canConnect ? (e.reason ?? tr('暂时无法连接')) : tr('流量已用完，当前限速 {0}', [formatSpeed(e.speedKbps)]),
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
      Text(tr('合计 {0} · 单日最高 {1}', [formatBytes(total), formatBytes(maxV)]),
          key: const Key('usage-total'), style: TextStyle(color: AppColors.sub, fontSize: 12)),
    ]);
  }
}
