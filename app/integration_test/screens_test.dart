// Visual tour for screenshots: prints "SHOT:<name>" and holds each screen for
// a few seconds so an external `xcrun simctl io booted screenshot` can grab it.
// Not part of CI; see README.
import 'package:flutter/material.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:vpn_app/main.dart';
import 'package:vpn_app/state/app_controller.dart';

const email = String.fromEnvironment('SHOT_EMAIL');
const password = String.fromEnvironment('SHOT_PASSWORD');

Future<void> hold(WidgetTester t, String name) async {
  for (var i = 0; i < 10; i++) {
    await t.pump(const Duration(milliseconds: 150));
  }
  // ignore: avoid_print
  print('SHOT:$name');
  final end = DateTime.now().add(const Duration(seconds: 4));
  while (DateTime.now().isBefore(end)) {
    await t.pump(const Duration(milliseconds: 100));
  }
}

Future<void> until(WidgetTester t, Finder f) async {
  for (var i = 0; i < 150 && f.evaluate().isEmpty; i++) {
    await t.pump(const Duration(milliseconds: 200));
  }
}

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  testWidgets('screenshot tour', (t) async {
    await const FlutterSecureStorage().deleteAll();
    final app = AppController();
    await t.pumpWidget(VpnApp(controller: app));
    await until(t, find.byKey(const Key('auth-submit')));
    await hold(t, 'login');
    await t.tap(find.text('注册'));
    await hold(t, 'register');

    await app.login(email, password);
    await until(t, find.byKey(const Key('connect-button')));
    await hold(t, 'home');

    await t.tap(find.byKey(const Key('node-card')));
    await until(t, find.text('选择节点'));
    await hold(t, 'nodes');
    await t.pageBack();
    await until(t, find.byKey(const Key('connect-button')));

    await t.tap(find.byKey(const Key('plan-chip')));
    await until(t, find.byKey(const Key('buy-button')));
    await hold(t, 'plans');
    await t.pageBack();
    await until(t, find.byKey(const Key('connect-button')));

    await t.tap(find.byKey(const Key('tab-应用')));
    await until(t, find.text('推荐应用'));
    await hold(t, 'apps');

    await t.tap(find.byKey(const Key('tab-我的')));
    await until(t, find.byKey(const Key('usage-total')));
    await hold(t, 'me');

    await t.tap(find.text('邀请好友送时长'));
    await until(t, find.byKey(const Key('ref-code')));
    await hold(t, 'referral');
  });
}
