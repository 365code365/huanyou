import 'package:flutter/material.dart';

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

class _MainShellState extends State<MainShell> {
  // The 应用 (recommendations) tab is hidden when the server offers none for
  // this client — which is the case on the iOS App Review face, where linking
  // out to other apps would fail review.
  bool _showApps = true;

  List<(GlassNavItem, WidgetBuilder)> get _tabs => [
    (GlassNavItem(label: tr('首页'), icon: Icons.radio_button_unchecked_rounded, activeIcon: Icons.radio_button_checked_rounded),
        (_) => const HomePage()),
    if (_showApps)
      (GlassNavItem(label: tr('应用'), icon: Icons.grid_view_outlined, activeIcon: Icons.grid_view_rounded), (_) => const AppsPage()),
    (GlassNavItem(label: tr('我的'), icon: Icons.person_outline_rounded, activeIcon: Icons.person_rounded), (_) => const MePage()),
  ];
  int _index = 0;
  final _built = <int>{0}; // build tabs lazily, then keep them alive

  @override
  void initState() {
    super.initState();
    _loadRecommendationsVisibility();
    // Ads, then the launch popup (if the admin enabled one), then the update
    // check, so two dialogs never stack.
    Future.delayed(const Duration(milliseconds: 900), () async {
      if (!mounted) return;
      await AdsStore.instance.load(AppScope.read(context).api);
      if (mounted) await maybeShowAdPopup(context);
      await Future.delayed(const Duration(milliseconds: 600));
      await _checkUpdate();
    });
  }

  Future<void> _loadRecommendationsVisibility() async {
    try {
      final apps = await AppScope.read(context).api.apps();
      // Only hide the tab when the server deliberately serves none (review
      // face); a transient load error leaves it shown.
      if (mounted && apps.isEmpty) {
        setState(() {
          _showApps = false;
          if (_index >= _tabs.length) _index = 0;
        });
      }
    } catch (_) {
      // keep the tab; AppsPage shows its own error / empty state
    }
  }

  Future<void> _checkUpdate() async {
    if (!mounted) return;
    try {
      final u = await Updater.check(AppScope.read(context).api);
      if (u != null && u.hasUpdate && mounted) await showUpdateDialog(context, u);
    } catch (_) {
      // offline / server without releases: try again next launch
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      extendBody: true,
      body: IndexedStack(
        index: _index,
        children: [
          for (var i = 0; i < _tabs.length; i++)
            _built.contains(i) ? _tabs[i].$2(context) : const SizedBox.shrink(),
        ],
      ),
      bottomNavigationBar: GlassNavBar(
        items: [for (final t in _tabs) t.$1],
        index: _index,
        onChanged: (i) => setState(() {
          _index = i;
          _built.add(i);
        }),
      ),
    );
  }
}
