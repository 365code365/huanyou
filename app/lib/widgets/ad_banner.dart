import 'dart:async';

import 'package:flutter/material.dart';

import '../core/analytics.dart';
import '../core/api.dart';
import '../core/open_url.dart';
import '../core/prefs.dart';
import '../i18n/i18n.dart';
import '../theme/app_theme.dart';

/// Ads configured in the admin panel (both slots are off by default).
class AdsStore extends ChangeNotifier {
  AdsStore._();
  static final AdsStore instance = AdsStore._();

  Ads? ads;
  DateTime? _loadedAt;

  Future<void> load(ApiClient api) async {
    if (_loadedAt != null && DateTime.now().difference(_loadedAt!) < const Duration(minutes: 10)) return;
    try {
      ads = await api.ads();
      _loadedAt = DateTime.now();
      notifyListeners();
    } catch (_) {
      // Offline / old server: no ads.
    }
  }
}

void _open(AdItem a) {
  if (a.link.isEmpty) return;
  Analytics.track('ad_click');
  openUrl(Uri.parse(a.link));
}

/// Home banner: one image, or an auto-advancing carousel when there are several.
class AdBanner extends StatefulWidget {
  const AdBanner({super.key});
  @override
  State<AdBanner> createState() => _AdBannerState();
}

class _AdBannerState extends State<AdBanner> {
  final _page = PageController();
  Timer? _timer;
  int _index = 0;
  int _count = 0;

  @override
  void initState() {
    super.initState();
    AdsStore.instance.addListener(_changed);
  }

  void _changed() {
    if (mounted) setState(() {});
  }

  void _arm(int count, int seconds) {
    if (count == _count && (_timer != null || count < 2)) return;
    _count = count;
    _timer?.cancel();
    _timer = null;
    if (count < 2) return;
    _timer = Timer.periodic(Duration(seconds: seconds.clamp(2, 60)), (_) {
      if (!mounted || !_page.hasClients) return;
      _page.animateToPage((_index + 1) % _count, duration: const Duration(milliseconds: 420), curve: Curves.easeOutCubic);
    });
  }

  @override
  void dispose() {
    AdsStore.instance.removeListener(_changed);
    _timer?.cancel();
    _page.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final ads = AdsStore.instance.ads;
    final items = ads?.home ?? const <AdItem>[];
    _arm(items.length, ads?.interval ?? 5);
    if (items.isEmpty) return const SizedBox.shrink();
    final dpr = MediaQuery.devicePixelRatioOf(context);
    return LayoutBuilder(builder: (context, c) {
      final w = c.maxWidth;
      return Column(children: [
        ClipRRect(
          borderRadius: BorderRadius.circular(AppRadius.card),
          child: AspectRatio(
            aspectRatio: 3,
            child: PageView.builder(
              controller: _page,
              itemCount: items.length,
              onPageChanged: (i) => setState(() => _index = i),
              itemBuilder: (context, i) {
                final a = items[i];
                return GestureDetector(
                  onTap: () => _open(a),
                  child: Semantics(
                    button: a.link.isNotEmpty,
                    label: a.title.isEmpty ? tr('广告') : a.title,
                    child: ColoredBox(
                      color: AppColors.sunken,
                      child: Image.network(
                        a.image,
                        fit: BoxFit.cover,
                        // Decode at display size: banners are often uploaded at 2–3x.
                        cacheWidth: (w * dpr).round(),
                        errorBuilder: (_, _, _) => const SizedBox.shrink(),
                      ),
                    ),
                  ),
                );
              },
            ),
          ),
        ),
        if (items.length > 1) ...[
          const SizedBox(height: 8),
          Row(mainAxisAlignment: MainAxisAlignment.center, children: [
            for (var i = 0; i < items.length; i++)
              AnimatedContainer(
                duration: const Duration(milliseconds: 200),
                margin: const EdgeInsets.symmetric(horizontal: 3),
                width: i == _index ? 16 : 6,
                height: 6,
                decoration: BoxDecoration(
                  color: i == _index ? AppColors.ink : AppColors.line,
                  borderRadius: BorderRadius.circular(3),
                ),
              ),
          ]),
        ],
      ]);
    });
  }
}

/// Launch popup / notice, shown at most as often as the admin set.
Future<void> maybeShowAdPopup(BuildContext context) async {
  final ads = AdsStore.instance.ads;
  final a = ads?.popup;
  if (a == null || !context.mounted) return;
  final key = 'popup_${a.id}';
  final today = DateTime.now().toIso8601String().substring(0, 10);
  final last = Prefs.get(key);
  switch (ads!.popupFrequency) {
    case 'once':
      if (last != null) return;
    case 'daily':
      if (last == today) return;
  }
  await Prefs.set(key, today);
  Analytics.track('ad_popup_shown');
  if (!context.mounted) return;
  await showDialog<void>(
    context: context,
    builder: (ctx) {
      final w = MediaQuery.sizeOf(ctx).width;
      return Dialog(
        clipBehavior: Clip.antiAlias,
        insetPadding: const EdgeInsets.symmetric(horizontal: 36, vertical: 48),
        child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.stretch, children: [
          if (a.image.isNotEmpty)
            GestureDetector(
              onTap: () {
                Navigator.pop(ctx);
                _open(a);
              },
              child: ConstrainedBox(
                constraints: BoxConstraints(maxHeight: MediaQuery.sizeOf(ctx).height * 0.55),
                child: Image.network(a.image,
                    fit: BoxFit.cover,
                    cacheWidth: (w * MediaQuery.devicePixelRatioOf(ctx)).round(),
                    errorBuilder: (_, _, _) => const SizedBox.shrink()),
              ),
            ),
          // Long admin text scrolls instead of overflowing small screens.
          Flexible(
            child: SingleChildScrollView(
              padding: const EdgeInsets.fromLTRB(20, 16, 20, 4),
              child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                if (a.title.isNotEmpty) Text(a.title, style: AppText.title),
                if (a.text.isNotEmpty) ...[
                  const SizedBox(height: 6),
                  Text(a.text, style: AppText.bodySmall.copyWith(color: AppColors.sub)),
                ],
              ]),
            ),
          ),
          Padding(
            padding: const EdgeInsets.fromLTRB(12, 0, 12, 8),
            child: Row(mainAxisAlignment: MainAxisAlignment.end, children: [
              TextButton(onPressed: () => Navigator.pop(ctx), child: Text(tr('关闭'))),
              if (a.link.isNotEmpty)
                FilledButton(
                  onPressed: () {
                    Navigator.pop(ctx);
                    _open(a);
                  },
                  child: Text(tr('查看详情')),
                ),
            ]),
          ),
        ]),
      );
    },
  );
}
