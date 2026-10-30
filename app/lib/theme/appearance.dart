import 'dart:ui' as ui;

import 'package:flutter/widgets.dart';
import '../core/prefs.dart';

import 'app_theme.dart';

/// Light / dark preference. "system" follows the OS setting live.
class Appearance extends ChangeNotifier {
  Appearance._();
  static final Appearance instance = Appearance._();

  static const _kMode = 'appearance';

  AppearanceMode mode = AppearanceMode.system;

  void load() => mode = AppearanceMode.parse(Prefs.get(_kMode));

  Future<void> set(AppearanceMode m) async {
    mode = m;
    notifyListeners();
    await Prefs.set(_kMode, m.id);
  }

  /// Whether the dark palette should be active right now.
  bool get isDark => switch (mode) {
        AppearanceMode.dark => true,
        AppearanceMode.light => false,
        AppearanceMode.system => ui.PlatformDispatcher.instance.platformBrightness == Brightness.dark,
      };
}
