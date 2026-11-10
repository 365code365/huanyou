import 'package:url_launcher/url_launcher.dart';

/// Opens a payment page in the system browser (which hands off to the
/// Alipay / WeChat app when installed). Replaceable in integration tests.
Future<bool> Function(Uri url) openUrl =
    (url) => launchUrl(url, mode: LaunchMode.externalApplication);

/// Opens a page inside the app (Safari view / Custom Tab); falls back to the
/// browser where that isn't available (desktop).
Future<bool> openInApp(Uri url) async {
  try {
    if (await launchUrl(url, mode: LaunchMode.inAppBrowserView)) return true;
  } catch (_) {}
  return openUrl(url);
}
