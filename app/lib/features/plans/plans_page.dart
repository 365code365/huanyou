import 'dart:async';

import 'package:flutter/material.dart';

import '../../core/api.dart';
import '../../core/format.dart';
import '../../core/iap.dart';
import '../../core/open_url.dart';
import '../../state/app_scope.dart';
import '../../theme/app_theme.dart';
import '../../widgets/primary_button.dart';
import '../../i18n/i18n.dart';
import '../../core/analytics.dart';

/// Opens the plans page; refreshes the user if a purchase completed.
Future<void> openPlans(BuildContext context) async {
  final app = AppScope.read(context);
  final bought = await Navigator.push<bool>(context, MaterialPageRoute(builder: (_) => const PlansPage()));
  if (bought == true) await app.refreshUser();
}

/// Paid plans + purchase flow: choose plan → choose method → open payment page → poll order.
class PlansPage extends StatefulWidget {
  const PlansPage({super.key});

  @override
  State<PlansPage> createState() => _PlansPageState();
}

class _PlansPageState extends State<PlansPage> {
  List<Plan>? _plans;
  List<PayMethod> _methods = [];
  String? _selected;
  String? _error;
  bool _buying = false;
  PurchaseState _purchase = const PurchaseState(enabled: true, notice: '');

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final (plans, methods, purchase) = await AppScope.read(context).api.plansFull();
      Analytics.track('plans_view');
      final paid = plans.where((p) => !p.free).toList();
      final current = AppScope.read(context).user?.entitlement;
      setState(() {
        _plans = paid;
        _methods = methods;
        _purchase = purchase;
        _selected = paid.any((p) => p.id == current?.planId) ? current!.planId : (paid.isEmpty ? null : paid.first.id);
      });
    } on ApiException catch (e) {
      setState(() => _error = e.message);
    }
  }

  Plan? get _plan => _plans?.where((p) => p.id == _selected).firstOrNull;

  Future<void> _buy() async {
    final plan = _plan;
    if (plan == null || !_purchase.enabled) return;
    // iOS App Review face: buy through Apple in-app purchase (StoreKit).
    if (_purchase.iap) return _buyIap(plan);
    if (_methods.isEmpty) return _snack(tr('暂无可用的支付方式'));
    Analytics.track('purchase_start');
    final method = _methods.length == 1 ? _methods.first : await _pickMethod(plan);
    if (method == null || !mounted) return;

    setState(() => _buying = true);
    try {
      final (order, payUrl) = await AppScope.read(context).api.createOrder(plan.id, method.id);
      if (!await openUrl(payUrl)) return _snack(tr('无法打开支付页面'));
      if (!mounted) return;
      final paid = await showDialog<bool>(
        context: context,
        barrierDismissible: false,
        builder: (_) => _PaymentDialog(orderId: order.id),
      );
      if (paid == true && mounted) {
        _snack(tr('支付成功，{0}已生效', [plan.name]));
        Navigator.pop(context, true);
      }
    } on ApiException catch (e) {
      _snack(e.message);
    } finally {
      if (mounted) setState(() => _buying = false);
    }
  }

  Future<void> _buyIap(Plan plan) async {
    Analytics.track('purchase_start');
    setState(() => _buying = true);
    try {
      await Iap.buy(
        productId: plan.iapProductId.isNotEmpty ? plan.iapProductId : plan.id,
        planId: plan.id,
        api: AppScope.read(context).api,
      );
      if (!mounted) return;
      _snack(tr('支付成功，{0}已生效', [plan.name]));
      Navigator.pop(context, true);
    } on IapException catch (e) {
      if (!e.cancelled) _snack(e.message);
    } catch (e) {
      _snack(tr('购买失败：{0}', [e]));
    } finally {
      if (mounted) setState(() => _buying = false);
    }
  }

  Future<PayMethod?> _pickMethod(Plan plan) => showModalBottomSheet<PayMethod>(
        context: context,
        showDragHandle: true,
        backgroundColor: AppColors.surface,
        builder: (ctx) => SafeArea(
          child: Padding(
            padding: const EdgeInsets.fromLTRB(20, 0, 20, 16),
            child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text(tr('支付 {0}', [formatYuan(plan.priceCents)]), style: const TextStyle(fontSize: 20, fontWeight: FontWeight.w700)),
              const SizedBox(height: 4),
              Text(plan.name, style: TextStyle(color: AppColors.sub)),
              const SizedBox(height: 16),
              for (final m in _methods)
                Padding(
                  padding: const EdgeInsets.only(bottom: 10),
                  child: Material(
                    color: AppColors.canvas,
                    borderRadius: BorderRadius.circular(14),
                    child: ListTile(
                      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(14)),
                      leading: Icon(
                        m.id.contains('wx') ? Icons.chat_rounded : Icons.account_balance_wallet_rounded,
                        color: m.id.contains('wx') ? const Color(0xFF07C160) : const Color(0xFF1677FF),
                      ),
                      title: Text(m.name),
                      trailing: const Icon(Icons.chevron_right_rounded),
                      onTap: () => Navigator.pop(ctx, m),
                    ),
                  ),
                ),
            ]),
          ),
        ),
      );

  void _snack(String msg) {
    if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(msg)));
  }

  @override
  Widget build(BuildContext context) {
    final plans = _plans;
    final cur = AppScope.of(context).user?.entitlement;
    return Scaffold(
      appBar: AppBar(title: Text(tr('购买套餐'))),
      body: _error != null
          ? Center(child: Text(_error!))
          : plans == null
              ? const Center(child: CircularProgressIndicator())
              : ListView(
                  padding: const EdgeInsets.fromLTRB(16, 8, 16, 120),
                  children: [
                    if (cur != null)
                      Padding(
                        padding: const EdgeInsets.fromLTRB(4, 0, 4, 14),
                        child: Text(
                          cur.paid && cur.expiresAt != null
                              ? tr('当前：{0}，{1} 到期。续费同一套餐顺延，换套餐立即生效。', [cur.planName, formatDate(cur.expiresAt!)])
                              : tr('当前：{0}', [cur.planName.isEmpty ? tr('无套餐') : cur.planName]),
                          style: TextStyle(color: AppColors.sub, fontSize: 13),
                        ),
                      ),
                    for (final p in plans)
                      Padding(
                        padding: const EdgeInsets.only(bottom: 12),
                        child: _PlanCard(
                          plan: p,
                          selected: p.id == _selected,
                          current: cur?.paid == true && cur?.planId == p.id,
                          onTap: () => setState(() => _selected = p.id),
                        ),
                      ),
                    if (plans.isEmpty)
                      Padding(
                        padding: EdgeInsets.only(top: 80),
                        child: Center(child: Text(tr('暂无在售套餐'), style: TextStyle(color: AppColors.sub))),
                      ),
                  ],
                ),
      bottomNavigationBar: _plan == null
          ? null
          : SafeArea(
              child: Container(
                padding: const EdgeInsets.fromLTRB(20, 12, 20, 12),
                decoration: BoxDecoration(
                  color: AppColors.surface,
                  border: Border(top: BorderSide(color: AppColors.line)),
                ),
                child: Row(children: [
                  Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.start, children: [
                    Text(tr('合计'), style: TextStyle(color: AppColors.sub, fontSize: 12)),
                    Text(formatYuan(_plan!.priceCents),
                        style: TextStyle(fontSize: 24, fontWeight: FontWeight.w800, color: AppColors.primary)),
                  ]),
                  const SizedBox(width: 20),
                  Expanded(
                    child: PrimaryButton(
                      key: const Key('buy-button'),
                      // Purchases closed in the admin panel: greyed out with the notice.
                      label: !_purchase.enabled
                          ? (_purchase.notice.isEmpty ? tr('暂不支持购买') : tr(_purchase.notice))
                          : cur?.paid == true && cur?.planId == _plan!.id
                              ? tr('立即续费')
                              : tr('立即购买'),
                      loading: _buying,
                      onPressed: _purchase.enabled ? _buy : null,
                    ),
                  ),
                ]),
              ),
            ),
    );
  }
}

class _PlanCard extends StatelessWidget {
  const _PlanCard({required this.plan, required this.selected, required this.current, required this.onTap});
  final Plan plan;
  final bool selected;
  final bool current;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final perMonth = plan.durationDays >= 60 ? plan.priceCents * 30 ~/ plan.durationDays : 0;
    final features = [
      tr('{0} / 周期', [formatTraffic(plan.trafficGb)]),
      formatSpeed(plan.speedMbps * 1000),
      tr('{0} 天', [plan.durationDays]),
    ];
    return AnimatedContainer(
      duration: const Duration(milliseconds: 180),
      decoration: BoxDecoration(
        color: AppColors.surface,
        borderRadius: BorderRadius.circular(AppRadius.card),
        border: Border.all(color: selected ? AppColors.primary : Colors.transparent, width: 1.8),
        boxShadow: [
          if (selected) BoxShadow(color: AppColors.shadow.withValues(alpha: 0.14), blurRadius: 18, offset: const Offset(0, 8)),
        ],
      ),
      child: Material(
        color: Colors.transparent,
        child: InkWell(
          key: Key('plan-${plan.id}'),
          borderRadius: BorderRadius.circular(AppRadius.card),
          onTap: onTap,
          child: Padding(
            padding: const EdgeInsets.all(18),
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
                Expanded(
                  child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                    Row(children: [
                      Flexible(
                        child: Text(plan.name,
                            maxLines: 1, overflow: TextOverflow.ellipsis, style: AppText.title),
                      ),
                      if (plan.hot) ...[
                        const SizedBox(width: 8),
                        const _HotBadge(),
                      ],
                      if (current) ...[
                        const SizedBox(width: 8),
                        Container(
                          padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
                          decoration: BoxDecoration(
                            color: AppColors.success.withValues(alpha: 0.12),
                            borderRadius: BorderRadius.circular(8),
                          ),
                          child: Text(tr('当前'), style: TextStyle(fontSize: 11, color: AppColors.success)),
                        ),
                      ],
                    ]),
                    if (plan.description.isNotEmpty) ...[
                      const SizedBox(height: 4),
                      Text(plan.description, style: TextStyle(color: AppColors.sub, fontSize: 13)),
                    ],
                  ]),
                ),
                Column(crossAxisAlignment: CrossAxisAlignment.end, children: [
                  Text(formatYuan(plan.priceCents),
                      style: TextStyle(
                          fontSize: 24, fontWeight: FontWeight.w800, color: selected ? AppColors.primary : AppColors.ink)),
                  if (perMonth > 0)
                    Text(tr('约 {0}/月', [formatYuan(perMonth)]), style: TextStyle(fontSize: 11, color: AppColors.sub)),
                ]),
              ]),
              const SizedBox(height: 14),
              Wrap(spacing: 8, runSpacing: 8, children: [
                for (final f in features)
                  Container(
                    padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 5),
                    decoration: BoxDecoration(color: AppColors.canvas, borderRadius: BorderRadius.circular(8)),
                    child: Text(f, style: TextStyle(fontSize: 12, color: AppColors.sub)),
                  ),
              ]),
            ]),
          ),
        ),
      ),
    );
  }
}

/// Polls the order every 2 s until it is paid (the provider confirms server-to-server).
class _PaymentDialog extends StatefulWidget {
  const _PaymentDialog({required this.orderId});
  final String orderId;

  @override
  State<_PaymentDialog> createState() => _PaymentDialogState();
}

class _PaymentDialogState extends State<_PaymentDialog> {
  Timer? _timer;
  bool _checking = false;
  String? _hint;
  final _started = DateTime.now();

  @override
  void initState() {
    super.initState();
    _timer = Timer.periodic(const Duration(seconds: 2), (_) => _check());
  }

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }

  Future<void> _check({bool manual = false}) async {
    if (_checking) return;
    _checking = true;
    try {
      final o = await AppScope.read(context).api.order(widget.orderId);
      if (!mounted) return;
      if (o.paid) {
        _timer?.cancel();
        Navigator.pop(context, true);
      } else if (manual) {
        setState(() => _hint = tr('还没有收到支付结果，请稍候'));
      } else if (DateTime.now().difference(_started) > const Duration(minutes: 15)) {
        _timer?.cancel();
        setState(() => _hint = tr('等待超时。如已付款，请稍后在「我的」查看'));
      }
    } on ApiException catch (e) {
      if (manual && mounted) setState(() => _hint = e.message);
    } finally {
      _checking = false;
    }
  }

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(22)),
      title: Text(tr('等待支付结果')),
      content: Column(mainAxisSize: MainAxisSize.min, children: [
        const LinearProgressIndicator(),
        const SizedBox(height: 16),
        Text(tr('请在打开的页面完成支付，完成后返回此处。'), style: TextStyle(color: AppColors.sub)),
        if (_hint != null) ...[const SizedBox(height: 8), Text(_hint!)],
      ]),
      actions: [
        TextButton(onPressed: () => Navigator.pop(context, false), child: Text(tr('取消'))),
        FilledButton(onPressed: () => _check(manual: true), child: Text(tr('我已完成支付'))),
      ],
    );
  }
}

/// 🔥 HOT: warm gradient pill on best-selling plans.
class _HotBadge extends StatelessWidget {
  const _HotBadge();
  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 2),
      decoration: BoxDecoration(
        gradient: const LinearGradient(colors: [Color(0xFFFF6A3D), Color(0xFFFF2D55)]),
        borderRadius: BorderRadius.circular(8),
      ),
      child: const Text('🔥 HOT',
          style: TextStyle(fontSize: 11, height: 1.3, fontWeight: FontWeight.w800, color: Colors.white, letterSpacing: 0.4)),
    );
  }
}
