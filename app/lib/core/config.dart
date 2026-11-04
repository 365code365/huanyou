/// Build-time configuration.
///
/// flutter run --dart-define=API_BASE_URLS=https://api1.example.com,https://api2.example.com
///
/// Several URLs are tried in order: if one API domain gets blocked or
/// DNS-poisoned in mainland China, the app falls back to the next.
class AppConfig {
  /// Product name. Also set in AndroidManifest (android:label), iOS Info.plist
  /// (CFBundleDisplayName) and the server's api.AppName.
  static const appName = '环游';
  static const slogan = '一键连接，畅游全球';

  /// Distribution channel, set at build time with --dart-define=CHANNEL=play.
  ///
  ///  - 'site' (default): the官网 APK / desktop build. External payment
  ///    (支付宝 / 易支付 / 网关), in-app self-update, download page and
  ///    recommended apps are available.
  ///  - 'play': the Google Play build. Google Play Billing only, no external
  ///    payment, no self-update, no external download links or recommended
  ///    apps, and a VpnService disclosure before connecting — everything
  ///    Google Play policy requires. Behaviour is fixed at compile time, the
  ///    same for every user; it is never flipped by the server.
  static const channel = String.fromEnvironment('CHANNEL', defaultValue: 'site');

  /// The Google Play build (channel=play on Android).
  static bool get isPlayStore => channel == 'play';

  static const _raw = String.fromEnvironment(
    'API_BASE_URLS',
    // A release build made without --dart-define must still work: it gets
    // the production API. Debug builds talk to the host machine from the
    // Android emulator.
    // New domain first; the old one stays as a fallback while it still works.
    defaultValue: bool.fromEnvironment('dart.vm.product')
        ? 'https://rely-vpn-go.com,https://net-w.51aihelp.com'
        : 'http://10.0.2.2:8080',
  );

  static List<Uri> get apiBaseUrls => _raw
      .split(',')
      .map((s) => s.trim())
      .where((s) => s.isNotEmpty)
      .map(Uri.parse)
      .toList();
}
