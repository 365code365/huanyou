import 'dart:ui' as ui;

import 'package:flutter/widgets.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';

import 'app_theme.dart';

/// Light / dark preference. "system" follows the OS setting live.
class Appearance extends ChangeNotifier {
  Appearance._();
  static final Appearance instance = Appearance._();

  static const _storage = FlutterSecureStorage();
  static const _kMode = 'appearance';

  AppearanceMode mode = AppearanceMode.system;

  Future<void> load() async {
    try {
      mode = AppearanceMode.parse(await _storage.read(key: _kMode));
    } catch (_) {}
  }

  Future<void> set(AppearanceMode m) async {
    mode = m;
    notifyListeners();
    await _storage.write(key: _kMode, value: m.id);
  }

  /// Whether the dark palette should be active right now.
  bool get isDark => switch (mode) {
        AppearanceMode.dark => true,
        AppearanceMode.light => false,
        AppearanceMode.system => ui.PlatformDispatcher.instance.platformBrightness == Brightness.dark,
      };
}
