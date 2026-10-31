import 'dart:async';
import 'dart:io';

import 'package:in_app_purchase/in_app_purchase.dart';

import 'api.dart';
import '../i18n/i18n.dart';

/// Apple App Store in-app purchase (the iOS "A face" shown to App Review).
///
/// The plan is bought through StoreKit; Apple's receipt is then sent to the
/// server (`/api/v1/iap/verify`), which verifies it with Apple and grants the
/// plan. On non-iOS platforms every method is a no-op.
///
/// Setup required before this works in review / production:
///  - In App Store Connect create an in-app purchase product per paid plan,
///    whose Product ID equals the plan's `iap_product_id` (the plan id, or the
///    plan's `ios_product_id` override).
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

  /// Buys [productId] for [planId]; once Apple confirms, verifies the receipt
  /// with the server so the plan is granted. Completes on success, throws
  /// [IapException] otherwise (cancelled=true when the user backed out).
  static Future<void> buy({required String productId, required String planId, required ApiClient api}) async {
    if (!await available()) throw IapException(tr('当前设备不支持 App 内购买'));
    final resp = await _iap.queryProductDetails({productId});
    ProductDetails? pd;
    for (final p in resp.productDetails) {
      if (p.id == productId) pd = p;
    }
    if (pd == null) throw IapException(tr('商品暂不可用，请稍后重试'));

    final done = Completer<void>();
    StreamSubscription<List<PurchaseDetails>>? sub;
    sub = _iap.purchaseStream.listen(
      (purchases) async {
        for (final p in purchases) {
          if (p.productID != productId) continue;
          switch (p.status) {
            case PurchaseStatus.purchased:
            case PurchaseStatus.restored:
              try {
                await api.verifyIap(planId, p.verificationData.serverVerificationData);
                if (p.pendingCompletePurchase) await _iap.completePurchase(p);
                if (!done.isCompleted) done.complete();
              } catch (e) {
                // Finish the StoreKit transaction so it isn't retried forever;
                // the server grant is idempotent, so a later retry is safe.
                if (p.pendingCompletePurchase) await _iap.completePurchase(p);
                if (!done.isCompleted) done.completeError(IapException(tr('购买校验失败，请稍后在「我的」查看')));
              }
            case PurchaseStatus.error:
              if (p.pendingCompletePurchase) await _iap.completePurchase(p);
              if (!done.isCompleted) done.completeError(IapException(p.error?.message ?? tr('购买失败')));
            case PurchaseStatus.canceled:
              if (!done.isCompleted) done.completeError(IapException(tr('已取消'), cancelled: true));
            case PurchaseStatus.pending:
              break;
          }
        }
      },
      onError: (Object e) {
        if (!done.isCompleted) done.completeError(IapException('$e'));
      },
    );

    try {
      // VPN plans are fixed-duration (non-renewing): a consumable product.
      await _iap.buyConsumable(purchaseParam: PurchaseParam(productDetails: pd), autoConsume: true);
      await done.future.timeout(const Duration(minutes: 5));
    } on TimeoutException {
      throw IapException(tr('等待支付结果超时，如已扣款请稍后在「我的」查看'));
    } finally {
      await sub.cancel();
    }
  }
}
