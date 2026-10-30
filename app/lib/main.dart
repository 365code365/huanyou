import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'core/config.dart';
import 'features/shell/main_shell.dart';
import 'state/app_controller.dart';
import 'state/app_scope.dart';
import 'theme/app_theme.dart';
import 'widgets/bootstrap_gate.dart';

// App structure (see README "App 架构"):
//   core/      API client, session storage, native VPN bridge, formatting
//   state/     AppController (single source of truth) + AppScope (DI)
//   theme/     design tokens and ThemeData
//   widgets/   reusable UI pieces
//   features/  one folder per screen / flow

void main() {
  WidgetsFlutterBinding.ensureInitialized();
  SystemChrome.setSystemUIOverlayStyle(const SystemUiOverlayStyle(
    statusBarColor: Colors.transparent,
    statusBarIconBrightness: Brightness.dark,
  ));
  runApp(VpnApp(controller: AppController()));
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
    widget.controller.init();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed) widget.controller.onResumed();
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
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
        theme: AppTheme.light(),
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

  @override
  void initState() {
    super.initState();
    Future.delayed(const Duration(milliseconds: 1300), () {
      if (mounted) setState(() => _introDone = true);
    });
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
            opacity: CurvedAnimation(parent: anim, curve: const Interval(0.35, 1, curve: Curves.easeIn)),
            child: ScaleTransition(scale: Tween(begin: 1.12, end: 1.0).animate(anim), child: child),
          );
        }
        return ScaleTransition(
          scale: Tween(begin: 0.97, end: 1.0).animate(CurvedAnimation(parent: anim, curve: Curves.easeOutCubic)),
          child: child,
        );
      },
      child: app.loggedIn == true && _introDone
          ? const MainShell(key: ValueKey('shell'))
          : BootstrapGate(
              key: const ValueKey('gate'),
              failed: app.loadFailed,
              message: app.message,
              onRetry: app.deviceSignIn,
            ),
    );
  }
}
