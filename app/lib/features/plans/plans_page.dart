import 'dart:async';

import 'package:flutter/material.dart';
import 'package:in_app_purchase/in_app_purchase.dart' show ProductDetails;

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
  // iOS IAP face: Apple's product details per plan id (localized price).
  // Only plans Apple returns a product for are offered there.
  Map<String, ProductDetails> _apple = {};
  bool _restoring = false;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final (plans, methods, purchase) = await AppScope.read(context).api.plansFull();
      Analytics.track('plans_view');
      var paid = plans.where((p) => !p.free).toList();
      var apple = <String, ProductDetails>{};
      if (purchase.iap) {
        // App Store face: prices come from Apple (the user's storefront and
        // currency), never from our backend, so the page always matches what
        // the App Store charges.
        final byProduct = await Iap.products({for (final p in paid) _productId(p)});
        apple = {for (final p in paid) if (byProduct[_productId(p)] != null) p.id: byProduct[_productId(p)]!};
        paid = paid.where((p) => apple.containsKey(p.id)).toList();
      }
      if (!mounted) return;
      AppScope.read(context).setIapFace(purchase.iap);
      final current = AppScope.read(context).user?.entitlement;
      setState(() {
        _error = null;
        _plans = paid;
        _methods = methods;
        _purchase = purchase;
        _apple = apple;
        _selected = paid.any((p) => p.id == current?.planId) ? current!.planId : (paid.isEmpty ? null : paid.first.id);
      });
    } on ApiException catch (e) {
      if (mounted) setState(() => _error = e.message);
    } catch (e) {
      // e.g. StoreKit unavailable: never leave the page on a spinner.
      Analytics.reportError(e, StackTrace.current, kind: 'plans');
      if (mounted) setState(() => _error = tr('加载失败，请重试'));
    }
  }

  void _retry() {
    setState(() {
      _error = null;
      _plans = null;
    });
    _load();
  }

  Plan? get _plan => _plans?.where((p) => p.id == _selected).firstOrNull;

  static String _productId(Plan p) => p.iapProductId.isNotEmpty ? p.iapProductId : p.id;

  /// The price shown for [p]: Apple's localized price on the IAP face, our
  /// backend price on the web face.
  String _price(Plan p) => _purchase.iap ? (_apple[p.id]?.price ?? '') : formatYuan(p.priceCents);

  /// "约 ¥20/月" for plans of two months or more ("" otherwise).
  String _perMonth(Plan p) {
    if (p.durationDays < 60) return '';
    if (!_purchase.iap) return tr('约 {0}/月', [formatYuan(p.priceCents * 30 ~/ p.durationDays)]);
    final pd = _apple[p.id];
    if (pd == null) return '';
    final v = pd.rawPrice * 30 / p.durationDays;
    return tr('约 {0}/月', ['${pd.currencySymbol}${v.toStringAsFixed(v >= 100 ? 0 : 2)}']);
  }

  Future<void> _restore() async {
    setState(() => _restoring = true);
    try {
      final app = AppScope.read(context);
      final r = await Iap.restore(app.api);
      await app.refreshUser();
      if (!mounted) return;
      _snack(r.linkedTo.isNotEmpty
          ? tr('已恢复：本机已加入原购买账号 {0} 的订阅', [r.linkedTo])
          : r.granted > 0
              ? tr('已恢复 {0} 笔购买', [r.granted])
              : tr('购买已恢复，套餐已生效'));
    } on IapException catch (e) {
      if (!e.cancelled) _snack(e.message);
    } on ApiException catch (e) {
      _snack(e.message);
    } catch (e) {
      Analytics.reportError(e, StackTrace.current, kind: 'iap_restore');
      _snack(tr('恢复失败，请稍后重试'));
    } finally {
      if (mounted) setState(() => _restoring = false);
    }
  }

  Future<void> _openLegal(String url) async {
    if (url.isEmpty || !await openUrl(Uri.parse(url))) _snack(tr('无法打开页面'));
  }

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
        productId: _productId(plan),
        planId: plan.id,
        api: AppScope.read(context).api,
      );
      if (!mounted) return;
      _snack(tr('支付成功，{0}已生效', [plan.name]));
      Navigator.pop(context, true);
    } on IapException catch (e) {
      if (!e.cancelled) _snack(e.message);
    } catch (e) {
      Analytics.reportError(e, StackTrace.current, kind: 'iap_buy');
      _snack(tr('购买失败，请重试'));
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
          ? _ErrorRetry(message: _error!, onRetry: _retry)
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
                          price: _price(p),
                          perMonth: _perMonth(p),
                          selected: p.id == _selected,
                          current: cur?.paid == true && cur?.planId == p.id,
                          onTap: () => setState(() => _selected = p.id),
                        ),
                      ),
                    if (plans.isEmpty)
                      Padding(
                        padding: const EdgeInsets.only(top: 80),
                        child: Column(children: [
                          Text(_purchase.iap ? tr('App Store 商品暂不可用，请稍后重试') : tr('暂无在售套餐'),
                              style: TextStyle(color: AppColors.sub)),
                          if (_purchase.iap) TextButton(onPressed: _retry, child: Text(tr('重试'))),
                        ]),
                      ),
                    if (plans.isNotEmpty) _Terms(
                      purchase: _purchase,
                      plan: _plan,
                      price: _plan == null ? '' : _price(_plan!),
                      restoring: _restoring,
                      onRestore: _restore,
                      onOpen: _openLegal,
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
                    Text(_price(_plan!),
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
  const _PlanCard(
      {required this.plan,
      required this.price,
      required this.perMonth,
      required this.selected,
      required this.current,
      required this.onTap});
  final Plan plan;
  final String price; // already formatted (Apple's price on the IAP face)
  final String perMonth; // "约 ¥20/月" or ""
  final bool selected;
  final bool current;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
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
                  Text(price,
                      style: TextStyle(
                          fontSize: 24, fontWeight: FontWeight.w800, color: selected ? AppColors.primary : AppColors.ink)),
                  if (perMonth.isNotEmpty) Text(perMonth, style: TextStyle(fontSize: 11, color: AppColors.sub)),
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

/// Purchase terms under the plans. On the App Store face this is what Apple
/// requires next to a subscription purchase: what is bought, how long, the
/// price, that it does not renew, who charges, and links to the terms and the
/// privacy policy, plus 恢复购买.
class _Terms extends StatelessWidget {
  const _Terms(
      {required this.purchase,
      required this.plan,
      required this.price,
      required this.restoring,
      required this.onRestore,
      required this.onOpen});
  final PurchaseState purchase;
  final Plan? plan;
  final String price;
  final bool restoring;
  final VoidCallback onRestore;
  final void Function(String url) onOpen;

  @override
  Widget build(BuildContext context) {
    final small = TextStyle(fontSize: 12, height: 1.6, color: AppColors.sub);
    final link = TextStyle(fontSize: 12, color: AppColors.primary, fontWeight: FontWeight.w600);
    final p = plan;
    return Padding(
      padding: const EdgeInsets.fromLTRB(4, 8, 4, 0),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        if (purchase.iap && p != null)
          Text(
            tr('「{0}」{1}，有效期 {2} 天，购买后立即生效。本套餐为非续期订阅，到期不会自动续费，可再次购买续期；续费同一套餐在原到期时间上顺延。付款将在确认购买时从您的 Apple ID 账户扣除。',
                [p.name, price, p.durationDays]),
            style: small,
          )
        else if (p != null)
          Text(tr('「{0}」{1}，有效期 {2} 天，付款成功后立即生效，到期不会自动扣费。', [p.name, price, p.durationDays]), style: small),
        const SizedBox(height: 8),
        Wrap(crossAxisAlignment: WrapCrossAlignment.center, spacing: 4, children: [
          if (purchase.termsUrl.isNotEmpty)
            InkWell(onTap: () => onOpen(purchase.termsUrl), child: Text(tr('《用户协议》'), style: link)),
          if (purchase.privacyUrl.isNotEmpty)
            InkWell(onTap: () => onOpen(purchase.privacyUrl), child: Text(tr('《隐私政策》'), style: link)),
          if (purchase.iap) ...[
            Text('·', style: small),
            restoring
                ? const SizedBox.square(dimension: 14, child: CircularProgressIndicator(strokeWidth: 2))
                : InkWell(
                    key: const Key('restore-purchases'), onTap: onRestore, child: Text(tr('恢复购买'), style: link)),
          ],
        ]),
        if (purchase.iap) ...[
          const SizedBox(height: 6),
          Text(tr('换了新手机？用购买时的 Apple ID 点「恢复购买」，或在原设备「我的 → 多设备」生成令牌让新设备加入。'), style: small),
        ],
      ]),
    );
  }
}

class _ErrorRetry extends StatelessWidget {
  const _ErrorRetry({required this.message, required this.onRetry});
  final String message;
  final VoidCallback onRetry;
  @override
  Widget build(BuildContext context) => Center(
        child: Padding(
          padding: const EdgeInsets.all(32),
          child: Column(mainAxisSize: MainAxisSize.min, children: [
            Text(message, textAlign: TextAlign.center, style: TextStyle(color: AppColors.sub)),
            const SizedBox(height: 12),
            OutlinedButton(onPressed: onRetry, child: Text(tr('重试'))),
          ]),
        ),
      );
}
