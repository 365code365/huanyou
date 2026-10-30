import 'package:flutter/material.dart';

/// Design tokens. Change colors / radii here, not in individual widgets.
///
/// Direction: calm and editorial rather than "glowing SaaS". Warm paper
/// background, near-black ink for primary actions, and a single signal green
/// that only appears when the tunnel is actually up. No decorative gradients.
class AppColors {
  static const ink = Color(0xFF15171C); // primary text + primary actions
  static const ink2 = Color(0xFF2A2D35);
  static const sub = Color(0xFF6E6F73); // secondary text
  static const faint = Color(0xFFA6A6A8);
  static const line = Color(0xFFE8E6E0);
  static const surface = Colors.white;
  static const night = Color(0xFF0D0F13); // splash / native launch screen background
  static const canvas = Color(0xFFF5F4F0); // warm paper page background
  static const sunken = Color(0xFFEFEDE7); // inputs, chips, empty bars

  static const primary = ink; // kept for existing call sites
  static const primaryDeep = ink2;
  static const signal = Color(0xFF12A150); // connected
  static const success = signal;
  static const warning = Color(0xFFD9822B);
  static const danger = Color(0xFFD64545);
  static const violet = Color(0xFFB0662C); // gift / referral accent (warm copper)

  /// Dark "card" fill. Named gradient for compatibility; intentionally subtle.
  static const brandGradient = LinearGradient(
    colors: [Color(0xFF1B1E24), Color(0xFF2B2F38)],
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
  static ThemeData light() {
    final scheme = ColorScheme.fromSeed(
      seedColor: AppColors.ink,
      primary: AppColors.ink,
      onPrimary: Colors.white,
      surface: AppColors.surface,
      error: AppColors.danger,
    );
    final base = ThemeData(useMaterial3: true, colorScheme: scheme, splashFactory: InkSparkle.splashFactory);
    return base.copyWith(
      scaffoldBackgroundColor: AppColors.canvas,
      textTheme: base.textTheme.apply(bodyColor: AppColors.ink, displayColor: AppColors.ink),
      appBarTheme: const AppBarTheme(
        backgroundColor: AppColors.canvas,
        surfaceTintColor: Colors.transparent,
        elevation: 0,
        scrolledUnderElevation: 0,
        centerTitle: false,
        foregroundColor: AppColors.ink,
        titleTextStyle: TextStyle(fontSize: 20, fontWeight: FontWeight.w700, color: AppColors.ink, letterSpacing: -0.2),
      ),
      cardTheme: CardThemeData(
        color: AppColors.surface,
        elevation: 0,
        margin: EdgeInsets.zero,
        surfaceTintColor: Colors.transparent,
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(AppRadius.card),
          side: const BorderSide(color: AppColors.line),
        ),
      ),
      filledButtonTheme: FilledButtonThemeData(
        style: FilledButton.styleFrom(
          backgroundColor: AppColors.ink,
          foregroundColor: Colors.white,
          shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(AppRadius.button)),
        ),
      ),
      textButtonTheme: TextButtonThemeData(style: TextButton.styleFrom(foregroundColor: AppColors.ink)),
      inputDecorationTheme: InputDecorationTheme(
        filled: true,
        fillColor: AppColors.sunken,
        contentPadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 16),
        hintStyle: const TextStyle(color: AppColors.faint),
        prefixIconColor: AppColors.faint,
        border: OutlineInputBorder(borderRadius: BorderRadius.circular(AppRadius.field), borderSide: BorderSide.none),
        enabledBorder: OutlineInputBorder(borderRadius: BorderRadius.circular(AppRadius.field), borderSide: BorderSide.none),
        focusedBorder: OutlineInputBorder(
          borderRadius: BorderRadius.circular(AppRadius.field),
          borderSide: const BorderSide(color: AppColors.ink, width: 1.2),
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
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      ),
      dialogTheme: DialogThemeData(
        backgroundColor: AppColors.surface,
        surfaceTintColor: Colors.transparent,
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(20)),
      ),
      progressIndicatorTheme: const ProgressIndicatorThemeData(color: AppColors.ink),
      dividerTheme: const DividerThemeData(color: AppColors.line, thickness: 1, space: 1),
    );
  }
}
