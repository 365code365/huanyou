/// Build-time configuration.
///
/// flutter run --dart-define=API_BASE_URLS=https://api1.example.com,https://api2.example.com
///
/// Several URLs are tried in order: if one API domain gets blocked or
/// DNS-poisoned in mainland China, the app falls back to the next.
class AppConfig {
  static const _raw = String.fromEnvironment(
    'API_BASE_URLS',
    defaultValue: 'http://10.0.2.2:8080', // Android emulator -> host machine
  );

  static List<Uri> get apiBaseUrls => _raw
      .split(',')
      .map((s) => s.trim())
      .where((s) => s.isNotEmpty)
      .map(Uri.parse)
      .toList();
}
