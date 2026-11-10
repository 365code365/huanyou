import 'package:flutter/material.dart';

import '../../core/prefs.dart';
import '../../core/updater.dart';
import '../../state/app_scope.dart';
import '../../widgets/glass_nav_bar.dart';
import '../apps/apps_page.dart';
import '../home/home_page.dart';
import '../me/me_page.dart';
import '../update/update_dialog.dart';
import '../../i18n/i18n.dart';
import '../../widgets/ad_banner.dart';

/// Bottom-tab shell with the floating liquid-glass bar. To add a tab, append to [_tabs].
///
/// Pages scroll underneath the bar (extendBody); they read
/// `MediaQuery.paddingOf(context).bottom` for the space the bar covers.
class MainShell extends StatefulWidget {
  const MainShell({super.key});

  @override
  State<MainShell> createState() => _MainShellState();
}

class _MainShellState extends State<MainShell> with WidgetsBindingObserver {
  // The 应用 (recommendations) tab is hidden when the server offers none for
  // this client — which is the case on the iOS App Review face, where linking
  // out to other apps would fail review. The last answer is cached, so the
  // tab does not flash in and out at every start.
  static const _kAppsTab = 'apps_tab';
  bool _showApps = Prefs.get(_kAppsTab) != '0';

  // Tabs are identified by id, not position: when 应用 appears or goes away,
  // the user stays on the tab they were on.
  List<(String, GlassNavItem, WidgetBuilder)> get _tabs => [
    ('home', GlassNavItem(label: tr('首页'), icon: Icons.radio_button_unchecked_rounded, activeIcon: Icons.radio_button_checked_rounded),
        (_) => const HomePage()),
    if (_showApps)
      ('apps', GlassNavItem(label: tr('应用'), icon: Icons.grid_view_outlined, activeIcon: Icons.grid_view_rounded), (_) => const AppsPage()),
    ('me', GlassNavItem(label: tr('我的'), icon: Icons.person_outline_rounded, activeIcon: Icons.person_rounded), (_) => const MePage()),
  ];
  String _current = 'home';
  final _built = <String>{'home'}; // build tabs lazily, then keep them alive

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    // Ads load first (popup / announcements / banner), before anything else
    // asks the server. The popup waits for the first frame, then the update
    // check runs after it, so two dialogs never stack.
    final ads = AdsStore.instance.load(AppScope.read(context).api);
    _loadRecommendationsVisibility();
    WidgetsBinding.instance.addPostFrameCallback((_) async {
      await ads;
      if (mounted) await maybeShowAdPopup(context);
      await Future.delayed(const Duration(milliseconds: 600));
      await _checkUpdate();
    });
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed) _refreshHome();
  }

  /// Back on the home tab (tab switch or app back in the foreground): pick up
  /// ads the admin published meanwhile and show a new popup if there is one.
  Future<void> _refreshHome() async {
    if (!mounted || _current != 'home') return;
    await AdsStore.instance.load(AppScope.read(context).api);
    if (mounted && _current == 'home') await maybeShowAdPopup(context);
  }

  Future<void> _loadRecommendationsVisibility() async {
    try {
      final apps = await AppScope.read(context).api.apps();
      // Only hide the tab when the server deliberately serves none (review
      // face); a transient load error keeps the last known state.
      final show = apps.isNotEmpty;
      Prefs.set(_kAppsTab, show ? '1' : '0');
      if (mounted && show != _showApps) {
        setState(() {
          _showApps = show;
          if (!show && _current == 'apps') _current = 'home';
          _built.remove('apps');
        });
      }
    } catch (_) {
      // keep the tab state; AppsPage shows its own error / empty state
    }
  }

  Future<void> _checkUpdate() async {
    if (!mounted) return;
    // App Store builds are updated by the App Store only.
    if (AppScope.read(context).iapFace) return;
    try {
      final u = await Updater.check(AppScope.read(context).api);
      if (u != null && u.hasUpdate && mounted) await showUpdateDialog(context, u);
    } catch (_) {
      // offline / server without releases: try again next launch
    }
  }

  @override
  Widget build(BuildContext context) {
    final tabs = _tabs;
    final index = tabs.indexWhere((t) => t.$1 == _current).clamp(0, tabs.length - 1);
    return Scaffold(
      extendBody: true,
      body: IndexedStack(
        index: index,
        children: [
          for (final t in tabs)
            KeyedSubtree(
              key: ValueKey(t.$1),
              child: _built.contains(t.$1) ? t.$3(context) : const SizedBox.shrink(),
            ),
        ],
      ),
      bottomNavigationBar: GlassNavBar(
        items: [for (final t in tabs) t.$2],
        index: index,
        onChanged: (i) {
          final was = _current;
          setState(() {
            _current = tabs[i].$1;
            _built.add(_current);
          });
          if (_current == 'home' && was != 'home') _refreshHome();
        },
      ),
    );
  }
}
