import 'package:flutter/material.dart';

import '../../core/api.dart';
import '../../core/open_url.dart';
import '../../state/app_scope.dart';
import '../../theme/app_theme.dart';
import '../../i18n/i18n.dart';
import '../../core/analytics.dart';

/// "应用" tab: backend-configured recommended apps, grouped by category.
class AppsPage extends StatefulWidget {
  const AppsPage({super.key});

  @override
  State<AppsPage> createState() => _AppsPageState();
}

class _AppsPageState extends State<AppsPage> {
  List<AppRec>? _apps;
  String? _error;

  @override
  void initState() {
    super.initState();
    Analytics.track('apps_view');
    _load();
  }

  Future<void> _load() async {
    try {
      final a = await AppScope.read(context).api.apps();
      if (mounted) {
        setState(() {
          _apps = a;
          _error = null;
        });
      }
    } on ApiException catch (e) {
      if (mounted) setState(() => _error = e.message);
    }
  }

  @override
  Widget build(BuildContext context) {
    final apps = _apps;
    final groups = <String, List<AppRec>>{};
    for (final a in apps ?? <AppRec>[]) {
      groups.putIfAbsent(a.category.isEmpty ? tr('其他') : a.category, () => []).add(a);
    }
    return Scaffold(
      appBar: AppBar(title: Text(tr('推荐应用'))),
      body: RefreshIndicator(
        onRefresh: _load,
        child: _error != null && apps == null
            ? ListView(children: [
                const SizedBox(height: 160),
                Center(child: Text(_error!, style: TextStyle(color: AppColors.sub))),
              ])
            : apps == null
                ? const Center(child: CircularProgressIndicator())
                : apps.isEmpty
                    ? ListView(children: [
                        SizedBox(height: 160),
                        Center(child: Text(tr('暂无推荐'), style: TextStyle(color: AppColors.sub))),
                      ])
                    : ListView(
                        padding: EdgeInsets.fromLTRB(16, 4, 16, MediaQuery.paddingOf(context).bottom + 24),
                        children: [
                          for (final e in groups.entries) ...[
                            Padding(
                              padding: const EdgeInsets.fromLTRB(4, 12, 4, 10),
                              child: Text(e.key, style: const TextStyle(fontSize: 15, fontWeight: FontWeight.w700)),
                            ),
                            GridView(
                              shrinkWrap: true,
                              physics: const NeverScrollableScrollPhysics(),
                              // Height from the content (grows with the system
                              // font size), not a fixed aspect ratio that
                              // overflows on small screens / large fonts.
                              gridDelegate: SliverGridDelegateWithFixedCrossAxisCount(
                                crossAxisCount: 2,
                                mainAxisSpacing: 12,
                                crossAxisSpacing: 12,
                                mainAxisExtent: _AppCard.height(context),
                              ),
                              children: [for (final a in e.value) _AppCard(app: a)],
                            ),
                          ],
                        ],
                      ),
      ),
    );
  }
}

class _AppCard extends StatelessWidget {
  const _AppCard({required this.app});
  final AppRec app;

  /// padding + icon + gap + title + subtitle, scaled like the text.
  static double height(BuildContext context) {
    final t = MediaQuery.textScalerOf(context);
    return 28 + 42 + 14 + t.scale(15) * 1.4 + 2 + t.scale(12) * 1.45 + 4;
  }

  @override
  Widget build(BuildContext context) {
    return Card(
      clipBehavior: Clip.antiAlias,
      child: InkWell(
        key: Key('app-${app.id}'),
        onTap: () {
          Analytics.track('app_rec_open');
          openUrl(Uri.parse(app.url));
        },
        child: Padding(
          padding: const EdgeInsets.all(14),
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Row(children: [
              ClipRRect(
                borderRadius: BorderRadius.circular(12),
                child: app.iconUrl.isEmpty
                    ? _fallback()
                    : Image.network(app.iconUrl,
                        width: 42,
                        height: 42,
                        fit: BoxFit.cover,
                        // Decode at display size, not the source size (icons are often 512 px).
                        cacheWidth: (42 * MediaQuery.devicePixelRatioOf(context)).round(),
                        errorBuilder: (ctx, err, st) => _fallback()),
              ),
              const Spacer(),
              Icon(Icons.north_east_rounded, size: 18, color: AppColors.faint),
            ]),
            const SizedBox(height: 14),
            Text(app.name, maxLines: 1, overflow: TextOverflow.ellipsis,
                style: const TextStyle(fontSize: 15, fontWeight: FontWeight.w600)),
            const SizedBox(height: 2),
            Text(app.desc.isEmpty ? app.url : app.desc,
                maxLines: 1, overflow: TextOverflow.ellipsis, style: TextStyle(fontSize: 12, color: AppColors.sub)),
          ]),
        ),
      ),
    );
  }

  Widget _fallback() => Container(
        width: 42,
        height: 42,
        decoration: BoxDecoration(gradient: AppColors.brandGradient),
        alignment: Alignment.center,
        child: Text(app.name.isEmpty ? '?' : app.name.characters.first,
            style: const TextStyle(color: Colors.white, fontSize: 17, fontWeight: FontWeight.w700)),
      );
}
