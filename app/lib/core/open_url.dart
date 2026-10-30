import 'package:url_launcher/url_launcher.dart';

/// Opens a payment page in the system browser (which hands off to the
/// Alipay / WeChat app when installed). Replaceable in integration tests.
Future<bool> Function(Uri url) openUrl =
    (url) => launchUrl(url, mode: LaunchMode.externalApplication);
