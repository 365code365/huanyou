// End-to-end UI test against a running API server (with payments.mock enabled).
//
// flutter test integration_test/app_test.dart -d <simulator-or-device> \
//   --dart-define=API_BASE_URLS=http://127.0.0.1:18080 \
//   --dart-define=E2E_NODE_TOKEN=<a node token> --dart-define=E2E_NODE_NAME=<that node's name>
//
// Flow: register (with mismatched-password check) → free plan shown →
// node page → pick node → auto-connect (Simulator: expected "unsupported") →
// switch modes → buy a plan with mock payment → node reports traffic →
// "我的" shows usage and the order → apps tab → logout.
import 'dart:convert';
import 'dart:math';

import 'package:flutter/material.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:integration_test/integration_test.dart';
import 'package:vpn_app/core/config.dart';
import 'package:vpn_app/core/open_url.dart';
import 'package:vpn_app/main.dart';
import 'package:vpn_app/state/app_controller.dart';

const nodeToken = String.fromEnvironment('E2E_NODE_TOKEN');
const nodeName = String.fromEnvironment('E2E_NODE_NAME', defaultValue: '本地测试');

Future<void> pumpUntil(WidgetTester t, Finder f, {Duration timeout = const Duration(seconds: 25)}) async {
  final end = DateTime.now().add(timeout);
  while (DateTime.now().isBefore(end)) {
    await t.pump(const Duration(milliseconds: 200));
    if (f.evaluate().isNotEmpty) return;
  }
  final visible = find.byType(Text).evaluate().map((e) => (e.widget as Text).data ?? '').where((s) => s.isNotEmpty);
  final fields = find.byType(EditableText).evaluate().map((e) => '[${(e.widget as EditableText).controller.text}]');
  throw TestFailure('Timed out waiting for $f\nVisible texts: ${visible.join(' | ')}\nFields: ${fields.join(' ')}');
}

Future<void> settle(WidgetTester t) => t.pump(const Duration(milliseconds: 600));

/// Hides the keyboard, scrolls [f] into view and taps it.
Future<void> tapIt(WidgetTester t, Finder f) async {
  FocusManager.instance.primaryFocus?.unfocus();
  await settle(t);
  await t.ensureVisible(f.first);
  await t.pump(const Duration(milliseconds: 300));
  await t.tap(f.first);
}

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  testWidgets('register, pick node, switch mode, buy plan, see usage', (t) async {
    await const FlutterSecureStorage().deleteAll();
    final api = AppConfig.apiBaseUrls.first;
    final email = 'e2e${Random().nextInt(1 << 30)}@example.com';

    // Stand-in for the browser: "click" confirm on the mock cashier page.
    openUrl = (url) async {
      if (!url.path.startsWith('/pay/mock/')) return true;
      final html = (await http.get(url)).body;
      String field(String name) => RegExp('name="$name" value="([^"]+)"').firstMatch(html)!.group(1)!;
      final r = await http.post(api.replace(path: '/pay/notify/mock'),
          body: {'order_id': field('order_id'), 't': field('t'), 'amount': field('amount')});
      expect(r.body, 'success');
      return true;
    };

    final controller = AppController();
    await t.pumpWidget(VpnApp(controller: controller));
    await pumpUntil(t, find.byKey(const Key('auth-submit')));

    // --- register: validation first, then success ---
    await t.tap(find.text('注册'));
    await settle(t);
    await t.enterText(find.byKey(const Key('auth-email')), email);
    await t.enterText(find.byKey(const Key('auth-password')), 'password123');
    await t.enterText(find.byKey(const Key('auth-confirm')), 'password999');
    await tapIt(t, find.byKey(const Key('auth-submit')));
    await settle(t);
    expect(find.text('两次输入的密码不一致'), findsOneWidget);
    await tapIt(t, find.byKey(const Key('auth-confirm')));
    await settle(t);
    await t.enterText(find.byKey(const Key('auth-confirm')), 'password123');
    await settle(t);
    await tapIt(t, find.byKey(const Key('auth-submit')));
    await pumpUntil(t, find.byKey(const Key('connect-button')));
    await pumpUntil(t, find.textContaining('免费版'));
    expect(controller.mode, ConnectMode.direct, reason: 'direct is the default mode');

    // --- node page: pick the node → returns and auto-connects ---
    await t.tap(find.byKey(const Key('node-card')));
    await pumpUntil(t, find.text('选择节点'));
    expect(find.text('智能选择'), findsOneWidget);
    await t.tap(find.textContaining(nodeName).last);
    await pumpUntil(t, find.byKey(const Key('connect-button')));
    // The Simulator cannot run Network Extensions, so the auto-connect reports that.
    await pumpUntil(t, find.textContaining('模拟器不支持 VPN'));
    expect(find.byKey(const Key('node-card-name')), findsOneWidget);
    expect(find.textContaining(nodeName), findsWidgets);

    // --- "智能选择" switches to cluster mode ---
    await t.tap(find.byKey(const Key('node-card')));
    await pumpUntil(t, find.byKey(const Key('node-auto')));
    await t.tap(find.byKey(const Key('node-auto')));
    await pumpUntil(t, find.byKey(const Key('connect-button')));
    await settle(t);
    expect(controller.mode, ConnectMode.cluster);

    // --- mode tiles ---
    await t.tap(find.byKey(const Key('mode-ai')));
    await settle(t);
    expect(controller.mode, ConnectMode.ai);
    await t.tap(find.byKey(const Key('mode-direct')));
    await settle(t);
    expect(controller.mode, ConnectMode.direct);

    // --- buy "月付" via the plan chip ---
    await t.tap(find.byKey(const Key('plan-chip')));
    await pumpUntil(t, find.byKey(const Key('plan-month')));
    await t.tap(find.byKey(const Key('plan-month')));
    await settle(t);
    await t.tap(find.byKey(const Key('buy-button')));
    await settle(t);
    if (find.text('模拟支付（测试）').evaluate().isNotEmpty) await t.tap(find.text('模拟支付（测试）'));
    await pumpUntil(t, find.textContaining('支付成功'));
    await pumpUntil(t, find.textContaining('月付 · 剩'));

    // --- a node reports traffic; "我的" shows it ---
    if (nodeToken.isNotEmpty) {
      final id = controller.user!.id;
      final r = await http.post(api.replace(path: '/internal/v1/traffic'),
          headers: {'Authorization': 'Bearer $nodeToken'},
          body: jsonEncode({
            'traffic': [
              {'email': 'u$id', 'up': 5 << 20, 'down': 45 << 20}
            ]
          }));
      expect(r.statusCode, 200);
    }
    await t.tap(find.byKey(const Key('tab-我的')));
    await pumpUntil(t, find.byKey(const Key('usage-total')));
    await settle(t);
    if (nodeToken.isNotEmpty) {
      await pumpUntil(t, find.textContaining('已用 50.0 MB / 200 GB'));
      await pumpUntil(t, find.textContaining('合计 50.0 MB'));
    }
    await t.tap(find.text('购买记录'));
    await pumpUntil(t, find.textContaining('month · ¥25'));
    await t.pageBack();
    await settle(t);

    // --- apps tab ---
    await t.tap(find.byKey(const Key('tab-应用')));
    await pumpUntil(t, find.text('推荐应用'));

    // --- logout ---
    await t.tap(find.byKey(const Key('tab-我的')));
    await settle(t);
    ScaffoldMessenger.of(t.element(find.byKey(const Key('logout')))).clearSnackBars();
    await settle(t);
    await tapIt(t, find.byKey(const Key('logout')));
    await settle(t);
    await t.tap(find.text('退出'));
    await pumpUntil(t, find.byKey(const Key('auth-submit')));
  });
}
