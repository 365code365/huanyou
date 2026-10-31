import 'dart:async';
import 'dart:io';

import 'package:flutter/services.dart';
import 'package:in_app_purchase/in_app_purchase.dart';
import 'package:in_app_purchase_storekit/in_app_purchase_storekit.dart';
import 'api.dart';
import '../i18n/i18n.dart';

/// Apple App Store in-app purchase (the iOS "A face" shown to App Review).
///
/// The plan is bought through StoreKit; Apple's receipt is then sent to the
/// server (`/api/v1/iap/verify`), which verifies it with Apple and grants the
/// plan. On non-iOS platforms every method is a no-op.
///
/// Setup required before this works in review / production:
///  - In App Store Connect create a 非续期订阅 (non-renewing subscription)
///    product per paid plan, whose Product ID equals the plan's
///    `iap_product_id` (the plan id, or the plan's `ios_product_id` override).
///  - Enable the In-App Purchase capability in the iOS target.
///  - Set the App-Specific Shared Secret in the admin config (apple_iap).
class IapException implements Exception {
  IapException(this.message, {this.cancelled = false});
  final String message;
  final bool cancelled; // the user backed out (don't show an error)
  @override
  String toString() => message;
}

class Iap {
  static final InAppPurchase _iap = InAppPurchase.instance;

  static Future<bool> available() async {
    if (!Platform.isIOS) return false;
    try {
      return await _iap.isAvailable();
    } catch (_) {
      return false;
    }
  }

  /// Apple's product details (localized price, currency) for [ids], keyed by
  /// product id. Products Apple doesn't return (not created / not approved
  /// yet) are missing from the map. Cached for the session.
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

  /// "恢复购买": refreshes the App Store receipt of the signed-in Apple ID and
  /// lets the server restore what it contains.
  static Future<RestoreResult> restore(ApiClient api) async {
    if (!await available()) throw IapException(tr('当前设备不支持 App 内购买'));
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
  // One listener for the whole app run (StoreKit delivers transactions at any
  // time: an Ask-to-Buy approved later, a purchase interrupted by a crash,
  // a transaction left unfinished last run). A purchase started by [buy] is
  // verified for its plan; anything else is redeemed through the restore
  // endpoint, which grants every unredeemed plan in the receipt.

  static StreamSubscription<List<PurchaseDetails>>? _sub;
  static ApiClient? _api;
  static void Function()? _onGranted;
  // product id -> the buy() waiting for it
  static final Map<String, ({String planId, Completer<void> done})> _waiting = {};

  /// Starts listening for App Store transactions (iOS only; call once at startup).
  /// [onGranted] runs after the server granted a purchase that no buy() was
  /// waiting for, so the UI can refresh the account.
  static void listen(ApiClient api, {void Function()? onGranted}) {
    if (!Platform.isIOS || _sub != null) return;
    _api = api;
    _onGranted = onGranted;
    _sub = _iap.purchaseStream.listen((purchases) {
      for (final p in purchases) {
        _handle(p);
      }
    }, onError: (Object e) {
      for (final w in _waiting.values) {
        if (!w.done.isCompleted) w.done.completeError(IapException(tr('购买失败，请重试')));
      }
    });
  }

  static Future<void> _finish(PurchaseDetails p) async {
    if (!p.pendingCompletePurchase) return;
    try {
      await _iap.completePurchase(p);
    } catch (_) {
      // StoreKit delivers it again next launch; finishing then is harmless
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
        // A cancelled StoreKit transaction must be finished too, or the next
        // purchase of the same product fails ("duplicate product").
        await _finish(p);
        return fail(IapException(tr('已取消'), cancelled: true));
      case PurchaseStatus.error:
        await _finish(p);
        return fail(IapException(tr('购买失败，请重试')));
      case PurchaseStatus.purchased:
      case PurchaseStatus.restored:
        final api = _api;
        if (api == null) return;
        final receipt = p.verificationData.serverVerificationData;
        try {
          if (w != null) {
            await api.verifyIap(w.planId, receipt);
          } else {
            await api.restoreIap(receipt);
            _onGranted?.call();
          }
        } catch (_) {
          // Not finished on purpose: StoreKit delivers it again at the next
          // launch and it is redeemed then (the grant is idempotent), so a
          // paid purchase is never lost to a network error.
          return fail(IapException(tr('购买已完成，正在等待服务器确认，稍后会自动到账；也可在购买页点「恢复购买」')));
        }
        await _finish(p);
        if (w != null && !w.done.isCompleted) w.done.complete();
    }
  }

  /// Buys [productId] for [planId]; once Apple confirms, verifies the receipt
  /// with the server so the plan is granted. Completes on success, throws
  /// [IapException] otherwise (cancelled=true when the user backed out).
  static Future<void> buy({required String productId, required String planId, required ApiClient api}) async {
    if (!await available()) throw IapException(tr('当前设备不支持 App 内购买'));
    listen(api);
    final pd = (await products({productId}))[productId];
    if (pd == null) throw IapException(tr('商品暂不可用，请稍后重试'));

    final done = Completer<void>();
    _waiting[productId] = (planId: planId, done: done);
    try {
      // Plans are fixed-duration non-renewing subscriptions: they stay in the
      // App Store receipt, which is what makes 恢复购买 possible.
      try {
        await _iap.buyNonConsumable(purchaseParam: PurchaseParam(productDetails: pd));
      } on PlatformException {
        throw IapException(tr('App Store 暂时无法发起购买，请稍后重试'));
      }
      await done.future.timeout(const Duration(minutes: 5));
    } on TimeoutException {
      // The listener keeps running: if Apple confirms later it is granted then.
      throw IapException(tr('等待支付结果超时，如已扣款会自动到账，也可稍后点「恢复购买」'));
    } finally {
      if (identical(_waiting[productId]?.done, done)) _waiting.remove(productId);
    }
  }
}
