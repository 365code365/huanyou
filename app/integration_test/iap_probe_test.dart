// Diagnostic: asks StoreKit on a real device for the App Store products and
// prints the storefront, what was found and what was not. Not part of CI.
//
//   flutter test integration_test/iap_probe_test.dart -d <device udid>
import 'package:flutter_test/flutter_test.dart';
import 'package:in_app_purchase/in_app_purchase.dart';
import 'package:integration_test/integration_test.dart';

const _ids = String.fromEnvironment('IAP_IDS',
    defaultValue: 'app.cygnus6152.artichoke66.vpn.lite,app.cygnus6152.artichoke66.vpn.month,'
        'app.cygnus6152.artichoke66.vpn.season,app.cygnus6152.artichoke66.vpn.year');

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  testWidgets('probe App Store products', (t) async {
    final iap = InAppPurchase.instance;
    // ignore: avoid_print
    void out(Object o) => print('PROBE $o');
    out('available=${await iap.isAvailable()}');
    try {
      out('storefront=${await iap.countryCode()}');
    } catch (e) {
      out('storefront error: $e');
    }
    final r = await iap.queryProductDetails(_ids.split(',').toSet());
    out('error=${r.error?.code} ${r.error?.message} ${r.error?.details}');
    out('notFound=${r.notFoundIDs}');
    for (final p in r.productDetails) {
      out('found ${p.id} ${p.price} ${p.currencyCode} "${p.title}"');
    }
  });
}
