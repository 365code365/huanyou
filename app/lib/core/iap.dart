import 'dart:async';
import 'dart:io';

import 'package:flutter/services.dart';
import 'package:in_app_purchase/in_app_purchase.dart';
import 'package:in_app_purchase_android/in_app_purchase_android.dart';
import 'package:in_app_purchase_storekit/in_app_purchase_storekit.dart';
import 'api.dart';
import 'config.dart';
import '../i18n/i18n.dart';

/// Official app-store in-app purchase: Apple App Store (StoreKit) on iOS and
/// Google Play Billing on the Google Play Android build. Both go through the
/// `in_app_purchase` plugin; the server verifies the result and grants the
/// plan:
///
///  - iOS: non-renewing subscriptions bought with StoreKit, the App Store
///    receipt POSTed to `/api/v1/iap/verify`, verified with Apple.
///  - Google Play: one-time (consumable) products bought with Play Billing,
///    the purchase JSON POSTed to `/api/v1/iap/google/verify`, verified with
///    the Play Developer API.
///
/// On every other platform / channel (官网 APK, desktop) every method is a
/// no-op and the app uses the web payment flow instead.
///
/// Setup required before this works:
///   iOS  — create a 非续期订阅 per paid plan in App Store Connect whose
///          Product ID equals the plan's `iap_product_id`; enable the In-App
///          Purchase capability; set apple_iap in the admin config.
///   Play — create a 一次性商品 (one-time product) per paid plan in Play
///          Console whose product ID equals the plan's `gp_product_id`;
///          configure payments.google_play on the server.
class IapException implements Exception {
  IapException(this.message, {this.cancelled = false});
  final String message;
  final bool cancelled; // the user backed out (don't show an error)
  @override
  String toString() => message;
}

class Iap {
  static final InAppPurchase _iap = InAppPurchase.instance;

  /// True on the faces that buy through an official store: iOS (always) and
  /// the Google Play Android build (never the官网 APK or desktop).
  static bool get _supported => Platform.isIOS || (Platform.isAndroid && AppConfig.isPlayStore);

  static Future<bool> available() async {
    if (!_supported) return false;
    try {
      return await _iap.isAvailable();
    } catch (_) {
      return false;
    }
  }

  /// The store's product details (localized price, currency) for [ids], keyed
  /// by product id. Products the store doesn't return (not created / not
  /// approved yet) are missing from the map. Cached for the session.
  static final Map<String, ProductDetails> _products = {};

  static Future<Map<String, ProductDetails>> products(Set<String> ids) async {
    final missing = ids.where((id) => !_products.containsKey(id)).toSet();
    if (missing.isNotEmpty && await available()) {
      final resp = await _iap.queryProductDetails(missing);
      for (final p in resp.productDetails) {
        _products[p.id] = p;
      }
    }
    return {for (final id in ids) if (_products[id] != null) id: _products[id]!};
  }

  /// "恢复购买".
  ///   iOS  — refreshes the App Store receipt of the signed-in Apple ID and
  ///          lets the server restore what it contains.
  ///   Play — re-delivers past Play purchases through the purchase stream;
  ///          any purchase not yet granted (e.g. the app died before the
  ///          server confirmed it) is granted by the listener. Returns an
  ///          empty result because the grant happens asynchronously.
  static Future<RestoreResult> restore(ApiClient api) async {
    if (!await available()) throw IapException(tr('当前设备不支持 App 内购买'));
    if (Platform.isAndroid) {
      listen(api);
      await _iap.restorePurchases();
      return RestoreResult.empty();
    }
    final add = _iap.getPlatformAddition<InAppPurchaseStoreKitPlatformAddition>();
    PurchaseVerificationData? data;
    try {
      data = await add.refreshPurchaseVerificationData(); // may ask the user to sign in to the App Store
    } catch (_) {
      data = null;
    }
    final receipt = data?.serverVerificationData ?? '';
    if (receipt.isEmpty) throw IapException(tr('当前 Apple ID 没有可恢复的购买'));
    return api.restoreIap(receipt);
  }

  // ---- purchases ----
  //
  // One listener for the whole app run (the store delivers transactions at any
  // time: an Ask-to-Buy approved later, a purchase interrupted by a crash, a
  // transaction left unfinished last run). A purchase started by [buy] is
  // verified for its plan; anything else is redeemed through the restore path.

  static StreamSubscription<List<PurchaseDetails>>? _sub;
  static ApiClient? _api;
  static void Function()? _onGranted;
  // product id -> the buy() waiting for it
  static final Map<String, ({String planId, Completer<void> done})> _waiting = {};

  /// Starts listening for store transactions (iOS / Play only; call once at
  /// startup). [onGranted] runs after the server granted a purchase that no
  /// buy() was waiting for, so the UI can refresh the account.
  static void listen(ApiClient api, {void Function()? onGranted}) {
    if (!_supported || _sub != null) return;
    _api = api;
    if (onGranted != null) _onGranted = onGranted;
    _sub = _iap.purchaseStream.listen((purchases) {
      for (final p in purchases) {
        _handle(p);
      }
    }, onError: (Object e) {
      for (final w in _waiting.values) {
        if (!w.done.isCompleted) w.done.completeError(IapException(tr('购买失败，请重试')));
      }
    });
    if (Platform.isAndroid) {
      // Unlike StoreKit, Play does not redeliver an unfinished purchase on its
      // own: a purchase paid while the app died, or whose verify failed, sits
      // unconsumed until we ask. Ask once per run; it arrives on the stream
      // above and is granted there (the grant is idempotent).
      unawaited(_iap.restorePurchases().catchError((Object _) {}));
    }
  }

  /// Ends a purchase. On Android a granted purchase is consumed, so the same
  /// plan can be bought again (时长顺延); a purchase that is not granted is
  /// left alone (never consumed) so it can be redeemed later. On iOS this
  /// finishes the StoreKit transaction.
  static Future<void> _finish(PurchaseDetails p, {bool granted = false}) async {
    try {
      if (Platform.isAndroid) {
        if (!granted) return;
        final add = _iap.getPlatformAddition<InAppPurchaseAndroidPlatformAddition>();
        await add.consumePurchase(p);
        return;
      }
      if (p.pendingCompletePurchase) await _iap.completePurchase(p);
    } catch (_) {
      // The store delivers it again next launch; finishing then is harmless.
    }
  }

  /// Sends the purchase to the server for verification. iOS uses the App Store
  /// receipt + plan id; Google Play uses the purchase JSON (the server reads
  /// the product id and token from it and maps the product back to a plan).
  static Future<void> _verify(PurchaseDetails p, String? planId) async {
    final api = _api;
    if (api == null) return;
    if (Platform.isAndroid) {
      // Play Billing: localVerificationData is the purchase originalJson,
      // serverVerificationData is the purchase token. Send both the JSON and
      // the parsed fields so the server is robust to either.
      await api.verifyGooglePlay(
        data: p.verificationData.localVerificationData,
        productId: p.productID,
        token: p.verificationData.serverVerificationData,
      );
      return;
    }
    final receipt = p.verificationData.serverVerificationData;
    if (planId != null) {
      await api.verifyIap(planId, receipt);
    } else {
      await api.restoreIap(receipt);
    }
  }

  static Future<void> _handle(PurchaseDetails p) async {
    final w = _waiting[p.productID];
    void fail(IapException e) {
      if (w != null && !w.done.isCompleted) w.done.completeError(e);
    }

    switch (p.status) {
      case PurchaseStatus.pending:
        return; // Ask to Buy / bank approval: the final state comes later
      case PurchaseStatus.canceled:
        // A cancelled transaction must be finished too, or the next purchase
        // of the same product fails ("duplicate product").
        await _finish(p);
        return fail(IapException(tr('已取消'), cancelled: true));
      case PurchaseStatus.error:
        await _finish(p);
        return fail(IapException(tr('购买失败，请重试')));
      case PurchaseStatus.purchased:
      case PurchaseStatus.restored:
        if (_api == null) return;
        try {
          await _verify(p, w?.planId);
          if (w == null) _onGranted?.call();
        } catch (_) {
          // Not finished on purpose: the store delivers it again at the next
          // launch and it is redeemed then (the grant is idempotent), so a
          // paid purchase is never lost to a network error.
          return fail(IapException(tr('购买已完成，正在等待服务器确认，稍后会自动到账；也可在购买页点「恢复购买」')));
        }
        await _finish(p, granted: true);
        if (w != null && !w.done.isCompleted) w.done.complete();
    }
  }

  /// Buys [productId] for [planId]; once the store confirms, verifies the
  /// purchase with the server so the plan is granted. Completes on success,
  /// throws [IapException] otherwise (cancelled=true when the user backed out).
  static Future<void> buy({required String productId, required String planId, required ApiClient api}) async {
    if (!await available()) throw IapException(tr('当前设备不支持 App 内购买'));
    listen(api);
    final pd = (await products({productId}))[productId];
    if (pd == null) throw IapException(tr('商品暂不可用，请稍后重试'));

    final done = Completer<void>();
    _waiting[productId] = (planId: planId, done: done);
    try {
      final param = PurchaseParam(productDetails: pd);
      try {
        if (Platform.isAndroid) {
          // One-time products bought as consumables so the same plan can be
          // repurchased (时长顺延). autoConsume must stay off: the plugin
          // would consume before our server grants, and a consumed purchase
          // is never redelivered, so a failed verify would lose a paid plan.
          // _finish consumes it once the server has granted it.
          await _iap.buyConsumable(purchaseParam: param, autoConsume: false);
        } else {
          // iOS plans are fixed-duration non-renewing subscriptions: they stay
          // in the App Store receipt, which is what makes 恢复购买 possible.
          await _iap.buyNonConsumable(purchaseParam: param);
        }
      } on PlatformException {
        throw IapException(tr('商店暂时无法发起购买，请稍后重试'));
      }
      await done.future.timeout(const Duration(minutes: 5));
    } on TimeoutException {
      // The listener keeps running: if the store confirms later it is granted then.
      throw IapException(tr('等待支付结果超时，如已扣款会自动到账，也可稍后点「恢复购买」'));
    } finally {
      if (identical(_waiting[productId]?.done, done)) _waiting.remove(productId);
    }
  }
}
