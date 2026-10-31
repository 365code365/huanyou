import 'package:flutter/material.dart';
import '../../core/api.dart';
import '../../core/format.dart';
import '../../state/app_scope.dart';
import '../../theme/app_theme.dart';
import '../../i18n/i18n.dart';

class OrdersPage extends StatefulWidget {
  const OrdersPage({super.key});

  @override
  State<OrdersPage> createState() => _OrdersPageState();
}

class _OrdersPageState extends State<OrdersPage> {
  List<Order>? _orders;
  Map<String, String> _planNames = {};
  String? _error;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    final api = AppScope.read(context).api;
    try {
      final orders = await api.orders();
      // Plan names for the titles; the ids are only a fallback.
      Map<String, String> names = _planNames;
      try {
        final (plans, _, _) = await api.plansFull();
        names = {for (final p in plans) p.id: p.name};
      } catch (_) {}
      if (!mounted) return;
      setState(() {
        _orders = orders;
        _planNames = names;
        _error = null;
      });
    } catch (e) {
      if (mounted) setState(() => _error = e is ApiException ? e.message : tr('加载失败'));
    }
  }

  @override
  Widget build(BuildContext context) {
    final orders = _orders;
    return Scaffold(
      appBar: AppBar(title: Text(tr('购买记录'))),
      body: RefreshIndicator(
        color: AppColors.ink,
        onRefresh: _load,
        child: _error != null && orders == null
            ? ListView(children: [
                const SizedBox(height: 160),
                Center(child: Text(_error!, style: TextStyle(color: AppColors.sub))),
                const SizedBox(height: 12),
                Center(
                  child: OutlinedButton(
                    onPressed: () {
                      setState(() => _error = null);
                      _load();
                    },
                    child: Text(tr('重试')),
                  ),
                ),
              ])
            : orders == null
                ? const Center(child: CircularProgressIndicator())
                : orders.isEmpty
                    ? ListView(children: [
                        const SizedBox(height: 160),
                        Center(child: Text(tr('暂无购买记录'), style: TextStyle(color: AppColors.sub))),
                      ])
                    : ListView.separated(
                        padding: const EdgeInsets.all(16),
                        itemCount: orders.length,
                        separatorBuilder: (_, _) => const SizedBox(height: 10),
                        itemBuilder: (_, i) => _OrderTile(order: orders[i], planName: _planNames[orders[i].planId]),
                      ),
      ),
    );
  }
}

class _OrderTile extends StatelessWidget {
  const _OrderTile({required this.order, required this.planName});
  final Order order;
  final String? planName;

  @override
  Widget build(BuildContext context) {
    final o = order;
    final (icon, color, label) = o.paid
        ? (Icons.check_rounded, AppColors.success, tr('已支付'))
        : o.status == 'pending'
            ? (Icons.schedule_rounded, AppColors.warning, tr('待支付'))
            : (Icons.close_rounded, AppColors.faint, tr('已关闭'));
    return Card(
      child: ListTile(
        contentPadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 6),
        leading: CircleAvatar(backgroundColor: color.withValues(alpha: 0.12), child: Icon(icon, color: color)),
        title: Text('${planName ?? o.planId} · ${formatYuan(o.amountCents)}',
            key: Key('order-${o.id}'), style: const TextStyle(fontWeight: FontWeight.w600)),
        subtitle: Text('$label · ${formatDate(o.paidAt ?? o.createdAt)}\n${o.id}',
            style: TextStyle(fontSize: 12, color: AppColors.sub, height: 1.5)),
        isThreeLine: true,
      ),
    );
  }
}
