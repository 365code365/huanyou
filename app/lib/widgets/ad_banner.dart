import 'dart:async';

import 'package:flutter/material.dart';

import '../core/analytics.dart';
import '../core/api.dart';
import '../core/open_url.dart';
import '../core/prefs.dart';
import '../i18n/i18n.dart';
import '../theme/app_theme.dart';
import 'skeleton.dart';

/// Ads configured in the admin panel (both slots are off by default).
class AdsStore extends ChangeNotifier {
  AdsStore._();
  static final AdsStore instance = AdsStore._();

  Ads? ads;
  DateTime? _loadedAt;

  /// True once the first load has finished (or failed). Until then the home
  /// page shows skeletons, but only for slots the last answer had content in
  /// (remembered across launches), so nothing flashes when ads are off.
  bool settled = false;
  static const _kShape = 'ads_shape'; // "<banners>,<announcements>" of the last answer
  bool get expectBanner => _shape(0) > 0;
  bool get expectMarquee => _shape(1) > 0;
  int _shape(int i) => int.tryParse((Prefs.get(_kShape) ?? '').split(',').elementAtOrNull(i) ?? '') ?? 0;
  Future<void>? _loading;
  String _sig = '';

  /// Fetches the ads. Called at startup and again whenever the user comes
  /// back to the home tab or the app returns to the foreground, so new
  /// announcements / banners / popups show without restarting the app.
  /// Calls within [minGap] of the last load reuse it; concurrent calls share
  /// one request. Listeners are told only when something actually changed,
  /// so a scrolling announcement doesn't restart on every tab switch.
  Future<void> load(ApiClient api, {Duration minGap = const Duration(seconds: 10)}) {
    if (_loading != null) return _loading!;
    if (_loadedAt != null && DateTime.now().difference(_loadedAt!) < minGap) return Future.value();
    return _loading = () async {
      try {
        final next = await api.ads();
        _loadedAt = DateTime.now();
        Prefs.set(_kShape, '${next.home.length},${next.marquee.length}');
        final sig = _signature(next);
        if (sig != _sig) {
          _sig = sig;
          ads = next;
          notifyListeners();
        }
      } catch (_) {
        // Offline / old server: keep what we have.
      } finally {
        _loading = null;
        if (!settled) {
          settled = true;
          notifyListeners(); // drop the skeletons
        }
      }
    }();
  }

  static String _signature(Ads a) {
    String one(AdItem? i) => i == null ? '-' : [i.id, i.title, i.text, i.image, i.link, i.open].join('\u0001');
    return [
      a.interval,
      a.popupFrequency,
      one(a.popup),
      ...a.home.map(one),
      '|',
      ...a.marquee.map(one),
    ].join('\u0002');
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
    if (items.isEmpty) {
      if (!AdsStore.instance.settled && AdsStore.instance.expectMarquee) {
        return Padding(
          padding: EdgeInsets.symmetric(vertical: ((widget.gap - 34) / 2).clamp(4.0, 20.0)),
          child: const SizedBox(height: 34, child: Skeleton(radius: 17)),
        );
      }
      return SizedBox(height: widget.gap);
    }
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
    if (items.isEmpty) {
      // Banners were configured last time: hold their space until they arrive.
      if (!AdsStore.instance.settled && AdsStore.instance.expectBanner) {
        return const AspectRatio(aspectRatio: 3, child: Skeleton(radius: AppRadius.card));
      }
      return const SizedBox.shrink();
    }
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
                        loadingBuilder: (_, child, p) => p == null ? child : const Skeleton(),
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
bool _popupOpen = false;
final _popupsThisLaunch = <String>{}; // "every launch" = once per app start, not per tab switch

Future<void> maybeShowAdPopup(BuildContext context) async {
  final ads = AdsStore.instance.ads;
  final a = ads?.popup;
  if (a == null || !context.mounted || _popupOpen) return;
  final key = 'popup_${a.id}';
  final today = DateTime.now().toIso8601String().substring(0, 10);
  final last = Prefs.get(key);
  switch (ads!.popupFrequency) {
    case 'once':
      if (last != null) return;
    case 'daily':
      if (last == today) return;
    default:
      if (_popupsThisLaunch.contains(a.id)) return;
  }
  _popupsThisLaunch.add(a.id);
  await Prefs.set(key, today);
  Analytics.track('ad_popup_shown');
  if (!context.mounted) return;
  _popupOpen = true;
  try {
    await _showPopup(context, a);
  } finally {
    _popupOpen = false;
  }
}

Future<void> _showPopup(BuildContext context, AdItem a) async {
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
                    // Hold a portrait-sized space while the image downloads.
                    loadingBuilder: (_, child, p) => p == null
                        ? child
                        : SizedBox(height: MediaQuery.sizeOf(ctx).height * 0.42, child: const Skeleton()),
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
