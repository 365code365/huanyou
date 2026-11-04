import 'package:flutter/material.dart';
import 'core/open_url.dart';
import 'core/integrity.dart';
import 'package:flutter/services.dart';

import 'core/updater.dart';
import 'features/update/update_dialog.dart';
import 'core/config.dart';
import 'features/shell/main_shell.dart';
import 'state/app_controller.dart';
import 'state/app_scope.dart';
import 'dart:async';
import 'dart:ui';

import 'core/analytics.dart';
import 'core/prefs.dart';
import 'features/consent/vpn_disclosure_page.dart';
import 'i18n/i18n.dart';
import 'theme/app_theme.dart';
import 'theme/appearance.dart';
import 'widgets/bootstrap_gate.dart';
import 'widgets/desktop_frame.dart';

// App structure (see README "App 架构"):
//   core/      API client, session storage, native VPN bridge, formatting
//   state/     AppController (single source of truth) + AppScope (DI)
//   theme/     design tokens and ThemeData
//   widgets/   reusable UI pieces
//   features/  one folder per screen / flow

Future<void> main() async {
  // Every uncaught error (framework, async, platform) goes to the crash
  // report, which the user can switch off in 我的 → 设置.
  runZonedGuarded(_main, (e, s) => Analytics.reportError(e, s));
}

Future<void> _main() async {
  WidgetsFlutterBinding.ensureInitialized();
  FlutterError.onError = (d) {
    FlutterError.presentError(d);
    Analytics.reportError(d.exception, d.stack, kind: 'flutter');
  };
  PlatformDispatcher.instance.onError = (e, s) {
    Analytics.reportError(e, s);
    return true;
  };
  // One secure-storage read for everything (no network), so the first frame
  // is already in the right language and palette.
  await Future.wait([Prefs.load(), DesktopFrame.init(), Integrity.check()]);
  I18n.instance.load();
  Appearance.instance.load();
  AppColors.dark = Appearance.instance.isDark;
  // Decoded-image budget: the app shows a handful of small icons / QR codes;
  // Flutter's default (100 MB, 1000 images) is sized for galleries.
  PaintingBinding.instance.imageCache
    ..maximumSizeBytes = 24 << 20
    ..maximumSize = 200;
  SystemChrome.setSystemUIOverlayStyle(const SystemUiOverlayStyle(statusBarColor: Colors.transparent));
  if (Integrity.tampered) {
    // Signed by someone other than us: a repackaged app. Don't run it.
    runApp(const _TamperedApp());
    return;
  }
  runApp(VpnApp(controller: AppController()));
}

class _TamperedApp extends StatelessWidget {
  const _TamperedApp();

  @override
  Widget build(BuildContext context) {
    final base = AppConfig.apiBaseUrls.isEmpty ? null : AppConfig.apiBaseUrls.first;
    return MaterialApp(
      debugShowCheckedModeBanner: false,
      home: Scaffold(
        body: SafeArea(
          child: Padding(
            padding: const EdgeInsets.all(32),
            child: Column(mainAxisAlignment: MainAxisAlignment.center, children: [
              const Icon(Icons.gpp_bad_rounded, size: 56, color: Color(0xFFD64545)),
              const SizedBox(height: 16),
              Text(tr('安装包已被修改'), style: const TextStyle(fontSize: 20, fontWeight: FontWeight.w700)),
              const SizedBox(height: 10),
              Text(tr('这不是官方发布的{0}，为了你的账号和数据安全已停止运行。请卸载后从官网下载正版应用。', [AppConfig.appName]),
                  textAlign: TextAlign.center, style: const TextStyle(height: 1.5)),
              const SizedBox(height: 24),
              if (base != null)
                FilledButton(
                  onPressed: () => openUrl(base.replace(path: '/download')),
                  child: Text(tr('前往官网下载')),
                ),
            ]),
          ),
        ),
      ),
    );
  }
}

class VpnApp extends StatefulWidget {
  const VpnApp({super.key, required this.controller});
  final AppController controller;

  @override
  State<VpnApp> createState() => _VpnAppState();
}

class _VpnAppState extends State<VpnApp> with WidgetsBindingObserver {
  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    I18n.instance.addListener(_restyle);
    Appearance.instance.addListener(_restyle);
    widget.controller.init();
    Analytics.init(widget.controller.api, widget.controller.session).then((_) {
      Analytics.track('app_open');
      Analytics.flush();
    });
    I18n.instance.sync(widget.controller.api); // pick up new / updated language packs
  }

  /// Language or palette changed: tr() and AppColors are read directly by
  /// widgets, so mark the whole tree for rebuild.
  void _restyle() {
    if (!mounted) return;
    AppColors.dark = Appearance.instance.isDark;
    setState(() {});
    rebuildAll(context);
  }

  @override
  void didChangePlatformBrightness() {
    if (Appearance.instance.mode == AppearanceMode.system) _restyle();
  }

  @override
  void didChangeLocales(List<Locale>? locales) {
    if (I18n.instance.preference == I18n.system) I18n.instance.sync(widget.controller.api);
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed) {
      Analytics.track('app_resume');
      widget.controller.onResumed();
    } else if (state == AppLifecycleState.paused) {
      widget.controller.onPaused();
      Analytics.flush(); // send what we have before the OS may stop us
    }
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    I18n.instance.removeListener(_restyle);
    Appearance.instance.removeListener(_restyle);
    widget.controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return AppScope(
      controller: widget.controller,
      child: MaterialApp(
        title: AppConfig.appName,
        debugShowCheckedModeBanner: false,
        theme: AppTheme.current(),
        // Material's own strings stay built in; layout direction follows the pack (ar/fa/he are RTL).
        builder: (context, child) => Directionality(
          textDirection: I18n.instance.direction,
          child: DesktopFrame(child: _Adaptive(child: child!)),
        ),
        home: const _Root(),
      ),
    );
  }
}

/// No login screen: the app provisions a device account automatically.
/// The splash stays up until the account is ready and its intro animation has
/// played (≈1.3 s), then cross-fades into the main shell.
class _Root extends StatefulWidget {
  const _Root();

  @override
  State<_Root> createState() => _RootState();
}

class _RootState extends State<_Root> {
  bool _introDone = false;
  // Google Play build: the VpnService prominent disclosure must be accepted
  // once before the app can connect (Google Play policy).
  bool _needConsent = AppController.playStore && !VpnConsent.given;

  @override
  void initState() {
    super.initState();
    // The brand intro (ring, orbiting dot, name, slogan) always plays in full
    // (~1.5 s). The home screen is built behind it from the cached snapshot,
    // so it is ready the moment the splash lifts.
    Future.delayed(const Duration(milliseconds: 1500), () {
      if (mounted) setState(() => _introDone = true);
    });
    // If the account can't be set up (server unreachable, or this old version
    // no longer understood by it), still look for an update: the update
    // endpoint needs no login, and it is the only way out for such users.
    Future.delayed(const Duration(seconds: 8), _updateFromSplash);
  }

  bool _updateChecked = false;

  Future<void> _updateFromSplash() async {
    if (!mounted || _updateChecked) return;
    final app = AppScope.read(context);
    if (app.loggedIn == true) return; // the main shell checks as usual
    _updateChecked = true;
    try {
      final u = await Updater.check(app.api);
      if (u != null && u.hasUpdate && mounted && app.loggedIn != true) await showUpdateDialog(context, u);
    } catch (_) {
      _updateChecked = false; // offline: try again on the next retry
      Future.delayed(const Duration(seconds: 30), _updateFromSplash);
    }
  }

  @override
  Widget build(BuildContext context) {
    final app = AppScope.of(context);
    return AnimatedSwitcher(
      duration: const Duration(milliseconds: 520),
      // The dark splash zooms toward the viewer and fades; the app rises in
      // underneath it. Avoids the grey mid-frame of a plain cross-fade.
      layoutBuilder: (current, previous) => Stack(children: [...previous.reversed, ?current]),
      transitionBuilder: (child, anim) {
        final isSplash = child.key == const ValueKey('gate');
        if (isSplash) {
          // Played in reverse when leaving: 1 → 0.
          return FadeTransition(
            opacity: CurvedAnimation(
              parent: anim,
              curve: const Interval(0.35, 1, curve: Curves.easeIn),
            ),
            child: ScaleTransition(scale: Tween(begin: 1.12, end: 1.0).animate(anim), child: child),
          );
        }
        return ScaleTransition(
          scale: Tween(begin: 0.97, end: 1.0).animate(CurvedAnimation(parent: anim, curve: Curves.easeOutCubic)),
          child: child,
        );
      },
      child: app.loggedIn == true && _introDone
          ? (_needConsent
              ? VpnDisclosurePage(
                  key: const ValueKey('consent'),
                  onAccept: () => setState(() => _needConsent = false),
                )
              : const MainShell(key: ValueKey('shell')))
          : BootstrapGate(
              key: const ValueKey('gate'),
              failed: app.loadFailed,
              message: app.message,
              onRetry: app.deviceSignIn,
            ),
    );
  }
}

/// Keeps layouts sane on unusual screens: very large system font sizes are
/// capped (beyond 1.35x the fixed-size controls overflow), and on tablets /
/// landscape the phone-first UI is centered at a readable width.
class _Adaptive extends StatelessWidget {
  const _Adaptive({required this.child});
  final Widget child;

  static const maxWidth = 600.0;

  @override
  Widget build(BuildContext context) {
    final mq = MediaQuery.of(context);
    Widget out = MediaQuery.withClampedTextScaling(minScaleFactor: 0.85, maxScaleFactor: 1.35, child: child);
    final w = mq.size.width;
    if (w > maxWidth + 80) {
      final side = (w - maxWidth) / 2;
      out = ColoredBox(
        color: AppColors.canvas,
        child: Padding(
          padding: EdgeInsets.symmetric(horizontal: side),
          // The child keeps the full height and safe-area insets.
          child: MediaQuery(
            data: mq.copyWith(size: Size(maxWidth, mq.size.height)),
            child: out,
          ),
        ),
      );
    }
    return out;
  }
}
