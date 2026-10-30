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
  String? _error;

  @override
  void initState() {
    super.initState();
    AppScope.read(context).api.orders().then((o) => setState(() => _orders = o)).catchError((Object e) {
      setState(() => _error = e is ApiException ? e.message : tr('加载失败'));
    });
  }

  @override
  Widget build(BuildContext context) {
    final orders = _orders;
    return Scaffold(
      appBar: AppBar(title: Text(tr('购买记录'))),
      body: _error != null
          ? Center(child: Text(_error!))
          : orders == null
              ? const Center(child: CircularProgressIndicator())
              : orders.isEmpty
                  ? Center(child: Text(tr('暂无购买记录'), style: TextStyle(color: AppColors.sub)))
                  : ListView.separated(
                      padding: const EdgeInsets.all(16),
                      itemCount: orders.length,
                      separatorBuilder: (_, _) => const SizedBox(height: 10),
                      itemBuilder: (_, i) {
                        final o = orders[i];
                        return Card(
                          child: ListTile(
                            contentPadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 6),
                            leading: const CircleAvatar(
                              backgroundColor: Color(0x1A16C784),
                              child: Icon(Icons.check_rounded, color: AppColors.success),
                            ),
                            title: Text('${o.planId} · ${formatYuan(o.amountCents)}',
                                key: Key('order-${o.id}'), style: const TextStyle(fontWeight: FontWeight.w600)),
                            subtitle: Text('${formatDate(o.paidAt ?? o.createdAt)}  ${o.id}',
                                style: TextStyle(fontSize: 12, color: AppColors.sub)),
                          ),
                        );
                      },
                    ),
    );
  }
}
