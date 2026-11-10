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
  final u = Uri.tryParse(a.link);
  if (u == null) return;
  Analytics.track('ad_click');
  a.open == 'webview' ? openInApp(u) : openUrl(u);
}

/// Home-page scrolling announcements: each line scrolls across once, then
/// the next one. Tapping opens the line's link. Off unless the admin set it.
class MarqueeBar extends StatefulWidget {
  const MarqueeBar({super.key, required this.gap});
  final double gap; // vertical space this replaces when there is nothing to show

  @override
  State<MarqueeBar> createState() => _MarqueeBarState();
}

class _MarqueeBarState extends State<MarqueeBar> with SingleTickerProviderStateMixin {
  late final AnimationController _anim = AnimationController(vsync: this)..addStatusListener(_done);
  int _i = 0;
  double _box = 0, _text = 0;
  static final _style = TextStyle(fontSize: 13, color: AppColors.sub, fontWeight: FontWeight.w500);
  static const _speed = 45.0; // logical px per second

  @override
  void initState() {
    super.initState();
    AdsStore.instance.addListener(_changed);
  }

  void _changed() {
    if (!mounted) return;
    setState(() {
      _i = 0;
      _box = 0; // re-measure and restart
    });
  }

  List<AdItem> get _items => AdsStore.instance.ads?.marquee ?? const [];

  void _done(AnimationStatus s) {
    if (s != AnimationStatus.completed || !mounted || _items.isEmpty) return;
    setState(() {
      _i = (_i + 1) % _items.length;
      _box = 0;
    });
  }

  // Measures the line and (re)starts the scroll when the line or width changes.
  void _start(double box, String text) {
    if (box == _box) return;
    final tp = TextPainter(
      text: TextSpan(text: text, style: _style),
      maxLines: 1,
      textDirection: TextDirection.ltr,
    )..layout();
    _box = box;
    _text = tp.width;
    tp.dispose();
    _anim.duration = Duration(milliseconds: ((box + _text) / _speed * 1000).round().clamp(2000, 120000));
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted) _anim.forward(from: 0);
    });
  }

  @override
  void dispose() {
    AdsStore.instance.removeListener(_changed);
    _anim.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final items = _items;
    if (items.isEmpty) return SizedBox(height: widget.gap);
    final a = items[_i % items.length];
    return Padding(
      padding: EdgeInsets.symmetric(vertical: ((widget.gap - 34) / 2).clamp(4.0, 20.0)),
      child: GestureDetector(
        onTap: () => _open(a),
        child: Semantics(
          label: a.text,
          button: a.link.isNotEmpty,
          child: Container(
            height: 34,
            padding: const EdgeInsets.symmetric(horizontal: 12),
            decoration: BoxDecoration(color: AppColors.sunken, borderRadius: BorderRadius.circular(17)),
            child: Row(
              children: [
                Icon(Icons.campaign_outlined, size: 18, color: AppColors.sub),
                const SizedBox(width: 8),
                Expanded(
                  child: LayoutBuilder(
                    builder: (context, c) {
                      _start(c.maxWidth, a.text);
                      return ClipRect(
                        child: AnimatedBuilder(
                          animation: _anim,
                          builder: (_, child) => Transform.translate(
                            offset: Offset(c.maxWidth - (c.maxWidth + _text) * _anim.value, 0),
                            child: child,
                          ),
                          child: OverflowBox(
                            alignment: Alignment.centerLeft,
                            maxWidth: double.infinity,
                            child: Text(a.text, maxLines: 1, softWrap: false, style: _style),
                          ),
                        ),
                      );
                    },
                  ),
                ),
                if (a.link.isNotEmpty) Icon(Icons.chevron_right, size: 18, color: AppColors.sub),
              ],
            ),
          ),
        ),
      ),
    );
  }
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
  // A link-only popup set to open in the app: show the page itself.
  if (a.open == 'webview' && a.link.isNotEmpty && a.image.isEmpty && a.text.isEmpty) {
    _open(a);
    return;
  }
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
