// App Review screenshots of the plans page on the iOS in-app-purchase face,
// one per plan with that plan selected. StoreKit is replaced by a fake that
// returns USD prices, so this works before Apple serves the products; plans
// and texts come from the real API (iOS build >= payment.ios_review_build).
//
// Prints "SHOT:<plan id>" and holds the screen so an external
// `xcrun simctl io <udid> screenshot` can grab it. Not part of CI.
//
//   flutter test integration_test/iap_shots_test.dart -d <simulator> \
//     --dart-define=SHOT_PRICES=lite:1.99,month:3.99,season:9.99,year:34.99
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:in_app_purchase/in_app_purchase.dart' show InAppPurchase;
// ignore: depend_on_referenced_packages
import 'package:in_app_purchase_platform_interface/in_app_purchase_platform_interface.dart';
import 'package:integration_test/integration_test.dart';
import 'package:vpn_app/core/integrity.dart';
import 'package:vpn_app/core/prefs.dart';
import 'package:vpn_app/i18n/i18n.dart';
import 'package:vpn_app/main.dart';
import 'package:vpn_app/state/app_controller.dart';
import 'package:vpn_app/theme/app_theme.dart';
import 'package:vpn_app/theme/appearance.dart';

const _pricesArg = String.fromEnvironment('SHOT_PRICES', defaultValue: 'lite:1.99,month:3.99,season:9.99,year:34.99');

/// Plan id → USD price. Product ids may carry a bundle prefix
/// (app.x.y.vpn.month): matched by the last dot-separated part.
final Map<String, double> _prices = {
  for (final kv in _pricesArg.split(','))
    if (kv.contains(':')) kv.split(':')[0].trim(): double.parse(kv.split(':')[1]),
};

class _FakeStore extends InAppPurchasePlatform {
  @override
  Future<bool> isAvailable() async => true;

  @override
  Stream<List<PurchaseDetails>> get purchaseStream => const Stream.empty();

  @override
  Future<ProductDetailsResponse> queryProductDetails(Set<String> identifiers) async {
    final found = <ProductDetails>[];
    for (final id in identifiers) {
      final usd = _prices[id.split('.').last];
      if (usd == null) continue;
      found.add(ProductDetails(
        id: id,
        title: id,
        description: '',
        price: '\$${usd.toStringAsFixed(2)}',
        rawPrice: usd,
        currencyCode: 'USD',
        currencySymbol: '\$',
      ));
    }
    return ProductDetailsResponse(
      productDetails: found,
      notFoundIDs: identifiers.where((i) => !found.any((p) => p.id == i)).toList(),
    );
  }

  @override
  Future<void> restorePurchases({String? applicationUserName}) async {}
}

Future<void> _until(WidgetTester t, Finder f) async {
  for (var i = 0; i < 200 && f.evaluate().isEmpty; i++) {
    await t.pump(const Duration(milliseconds: 200));
  }
  if (f.evaluate().isEmpty) {
    final texts = find.byType(Text).evaluate().map((e) => (e.widget as Text).data ?? '').where((s) => s.isNotEmpty);
    throw TestFailure('Timed out waiting for $f\nVisible: ${texts.join(' | ')}');
  }
}

Future<void> _hold(WidgetTester t, String name) async {
  for (var i = 0; i < 12; i++) {
    await t.pump(const Duration(milliseconds: 150));
  }
  // ignore: avoid_print
  print('SHOT:$name');
  final end = DateTime.now().add(const Duration(seconds: 6));
  while (DateTime.now().isBefore(end)) {
    await t.pump(const Duration(milliseconds: 100));
  }
}

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  testWidgets('plans page screenshots (iOS IAP face)', (t) async {
    // InAppPurchase.instance registers StoreKit the first time it is read:
    // create it first, then swap the platform for the fake.
    InAppPurchase.instance;
    InAppPurchasePlatform.instance = _FakeStore();
    await Future.wait([Prefs.load(), Integrity.check()]);
    await Prefs.set('lang_pref', 'zh'); // Simplified Chinese whatever the simulator language
    I18n.instance.load();
    Appearance.instance.load();
    AppColors.dark = Appearance.instance.isDark;

    await t.pumpWidget(VpnApp(controller: AppController()));
    await _until(t, find.byKey(const Key('plan-chip')));
    await t.pump(const Duration(seconds: 2));
    await t.tap(find.byKey(const Key('plan-chip')));
    await _until(t, find.byKey(const Key('buy-button')));

    for (final id in _prices.keys) {
      final card = find.byKey(Key('plan-$id'));
      if (card.evaluate().isEmpty) continue;
      await t.ensureVisible(card);
      await t.pump(const Duration(milliseconds: 300));
      await t.tap(card);
      // Scroll back to the top so every shot shows the whole list.
      await t.drag(find.byType(ListView), const Offset(0, 2000));
      await _hold(t, id);
    }
  });
}
