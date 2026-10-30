import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

/// One set of surface/text colours. Light is warm paper with near-black ink;
/// dark is a neutral graphite with soft white ink. Accents are shared.
class AppPalette {
  const AppPalette({
    required this.ink,
    required this.ink2,
    required this.onInk,
    required this.sub,
    required this.faint,
    required this.line,
    required this.surface,
    required this.canvas,
    required this.sunken,
    required this.hero,
    required this.heroEnd,
  });
  final Color ink; // primary text + primary action fill
  final Color ink2;
  final Color onInk; // text/icons drawn on an ink fill
  final Color sub; // secondary text
  final Color faint;
  final Color line;
  final Color surface; // cards
  final Color canvas; // page background
  final Color sunken; // inputs, chips, empty bars
  final Color hero; // dark feature cards (member card, invite banner)
  final Color heroEnd;

  static const light = AppPalette(
    ink: Color(0xFF15171C),
    ink2: Color(0xFF2A2D35),
    onInk: Colors.white,
    sub: Color(0xFF6E6F73),
    faint: Color(0xFFA6A6A8),
    line: Color(0xFFE8E6E0),
    surface: Colors.white,
    canvas: Color(0xFFF5F4F0),
    sunken: Color(0xFFEFEDE7),
    hero: Color(0xFF15171C),
    heroEnd: Color(0xFF2B2F38),
  );

  static const dark = AppPalette(
    ink: Color(0xFFEDEDEF),
    ink2: Color(0xFFC9CACF),
    onInk: Color(0xFF111317),
    sub: Color(0xFF9A9BA1),
    faint: Color(0xFF6A6C72),
    line: Color(0xFF2A2D34),
    surface: Color(0xFF1A1C21),
    canvas: Color(0xFF0F1114),
    sunken: Color(0xFF24272D),
    hero: Color(0xFF1F2228),
    heroEnd: Color(0xFF2C3039),
  );
}

/// Design tokens. Change colours / radii here, not in individual widgets.
///
/// Surface and text tokens follow the active palette ([dark] is set by the
/// app root from the user's appearance setting); accents are fixed.
class AppColors {
  static bool dark = false;
  static AppPalette get _p => dark ? AppPalette.dark : AppPalette.light;

  static Color get ink => _p.ink;
  static Color get ink2 => _p.ink2;
  static Color get onInk => _p.onInk;
  static Color get sub => _p.sub;
  static Color get faint => _p.faint;
  static Color get line => _p.line;
  static Color get surface => _p.surface;
  static Color get canvas => _p.canvas;
  static Color get sunken => _p.sunken;
  static Color get hero => _p.hero;
  static Color get shadow => dark ? Colors.black : const Color(0xFF15171C); // drop shadows
  static Color get primary => ink; // kept for existing call sites
  static Color get primaryDeep => ink2;

  static const night = Color(0xFF0D0F13); // splash / native launch screen background
  static const signal = Color(0xFF12A150); // connected
  static const success = signal;
  static const warning = Color(0xFFD9822B);
  static const danger = Color(0xFFD64545);
  static const violet = Color(0xFFB0662C); // gift / referral accent (warm copper)

  /// Dark "card" fill. Named gradient for compatibility; intentionally subtle.
  static LinearGradient get brandGradient => LinearGradient(
        colors: [_p.hero, _p.heroEnd],
        begin: Alignment.topLeft,
        end: Alignment.bottomRight,
      );
  static const connectedGradient = LinearGradient(
    colors: [Color(0xFF119A4C), Color(0xFF16AE57)],
    begin: Alignment.topLeft,
    end: Alignment.bottomRight,
  );
}

class AppRadius {
  static const card = 18.0;
  static const field = 12.0;
  static const button = 14.0;
}

class AppTheme {
  /// Theme for the active palette (see [AppColors.dark]).
  static ThemeData current() {
    final dark = AppColors.dark;
    final scheme = ColorScheme.fromSeed(
      seedColor: AppColors.ink,
      brightness: dark ? Brightness.dark : Brightness.light,
      primary: AppColors.ink,
      onPrimary: AppColors.onInk,
      surface: AppColors.surface,
      onSurface: AppColors.ink,
      error: AppColors.danger,
    );
    final overlay = SystemUiOverlayStyle(
      statusBarColor: Colors.transparent,
      statusBarIconBrightness: dark ? Brightness.light : Brightness.dark,
      statusBarBrightness: dark ? Brightness.dark : Brightness.light, // iOS
      systemNavigationBarColor: AppColors.canvas,
      systemNavigationBarIconBrightness: dark ? Brightness.light : Brightness.dark,
    );
    final base = ThemeData(useMaterial3: true, colorScheme: scheme, splashFactory: InkSparkle.splashFactory);
    return base.copyWith(
      scaffoldBackgroundColor: AppColors.canvas,
      textTheme: base.textTheme.apply(bodyColor: AppColors.ink, displayColor: AppColors.ink),
      appBarTheme: AppBarTheme(
        backgroundColor: AppColors.canvas,
        surfaceTintColor: Colors.transparent,
        elevation: 0,
        scrolledUnderElevation: 0,
        centerTitle: false,
        foregroundColor: AppColors.ink,
        systemOverlayStyle: overlay,
        titleTextStyle: TextStyle(fontSize: 20, fontWeight: FontWeight.w700, color: AppColors.ink, letterSpacing: -0.2),
      ),
      cardTheme: CardThemeData(
        color: AppColors.surface,
        elevation: 0,
        margin: EdgeInsets.zero,
        surfaceTintColor: Colors.transparent,
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(AppRadius.card),
          side: BorderSide(color: AppColors.line),
        ),
      ),
      filledButtonTheme: FilledButtonThemeData(
        style: FilledButton.styleFrom(
          backgroundColor: AppColors.ink,
          foregroundColor: AppColors.onInk,
          shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(AppRadius.button)),
        ),
      ),
      textButtonTheme: TextButtonThemeData(style: TextButton.styleFrom(foregroundColor: AppColors.ink)),
      inputDecorationTheme: InputDecorationTheme(
        filled: true,
        fillColor: AppColors.sunken,
        contentPadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 16),
        hintStyle: TextStyle(color: AppColors.faint),
        prefixIconColor: AppColors.faint,
        border: OutlineInputBorder(borderRadius: BorderRadius.circular(AppRadius.field), borderSide: BorderSide.none),
        enabledBorder: OutlineInputBorder(borderRadius: BorderRadius.circular(AppRadius.field), borderSide: BorderSide.none),
        focusedBorder: OutlineInputBorder(
          borderRadius: BorderRadius.circular(AppRadius.field),
          borderSide: BorderSide(color: AppColors.ink, width: 1.2),
        ),
        errorBorder: OutlineInputBorder(
          borderRadius: BorderRadius.circular(AppRadius.field),
          borderSide: const BorderSide(color: AppColors.danger),
        ),
      ),
      navigationBarTheme: NavigationBarThemeData(
        backgroundColor: AppColors.canvas,
        surfaceTintColor: Colors.transparent,
        indicatorColor: AppColors.ink.withValues(alpha: 0.07),
        height: 62,
        labelTextStyle: WidgetStateProperty.resolveWith(
          (s) => TextStyle(
            fontSize: 11.5,
            fontWeight: s.contains(WidgetState.selected) ? FontWeight.w600 : FontWeight.w400,
            color: s.contains(WidgetState.selected) ? AppColors.ink : AppColors.faint,
          ),
        ),
        iconTheme: WidgetStateProperty.resolveWith(
          (s) => IconThemeData(color: s.contains(WidgetState.selected) ? AppColors.ink : AppColors.faint, size: 23),
        ),
      ),
      snackBarTheme: SnackBarThemeData(
        behavior: SnackBarBehavior.floating,
        backgroundColor: AppColors.ink,
        contentTextStyle: TextStyle(color: AppColors.onInk, fontSize: 14),
        actionTextColor: dark ? AppColors.signal : const Color(0xFF7FD8A4),
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      ),
      dialogTheme: DialogThemeData(
        backgroundColor: AppColors.surface,
        surfaceTintColor: Colors.transparent,
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(20)),
      ),
      bottomSheetTheme: BottomSheetThemeData(
        backgroundColor: AppColors.surface,
        surfaceTintColor: Colors.transparent,
        modalBackgroundColor: AppColors.surface,
      ),
      listTileTheme: ListTileThemeData(iconColor: AppColors.ink, textColor: AppColors.ink),
      progressIndicatorTheme: ProgressIndicatorThemeData(color: AppColors.ink),
      dividerTheme: DividerThemeData(color: AppColors.line, thickness: 1, space: 1),
    );
  }
}

/// The user's appearance choice: follow the system, or force light / dark.
enum AppearanceMode {
  system('system', '跟随系统'),
  light('light', '浅色'),
  dark('dark', '深色');

  const AppearanceMode(this.id, this.label);
  final String id;
  final String label; // Chinese source; translate with tr() at the call site
  static AppearanceMode parse(String? s) => values.firstWhere((m) => m.id == s, orElse: () => system);
}
