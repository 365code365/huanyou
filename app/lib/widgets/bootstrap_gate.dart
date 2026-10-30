import 'dart:math' as math;
import 'dart:ui';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../core/config.dart';
import '../theme/app_theme.dart';
import '../i18n/i18n.dart';

/// Launch screen. Continues the native splash (same dark background, same
/// mark position): the ring draws itself, the green dot travels once around
/// the orbit and settles in the gap, then the name and slogan rise in.
///
/// There is no login: the device account is provisioned behind this screen.
/// If that takes long, a quiet status line appears; on failure, a retry.
class BootstrapGate extends StatefulWidget {
  const BootstrapGate({super.key, required this.failed, required this.onRetry, this.message});

  final bool failed;
  final String? message;
  final Future<void> Function() onRetry;

  @override
  State<BootstrapGate> createState() => _BootstrapGateState();
}

class _BootstrapGateState extends State<BootstrapGate> with TickerProviderStateMixin {
  late final AnimationController _intro =
      AnimationController(vsync: this, duration: const Duration(milliseconds: 1500))..forward();
  late final AnimationController _ambient =
      AnimationController(vsync: this, duration: const Duration(seconds: 14))..repeat();
  bool _slow = false;

  @override
  void initState() {
    super.initState();
    Future.delayed(const Duration(milliseconds: 2600), () {
      if (mounted) setState(() => _slow = true);
    });
  }

  @override
  void dispose() {
    _intro.dispose();
    _ambient.dispose();
    super.dispose();
  }

  double _seg(double a, double b, [Curve c = Curves.easeOutCubic]) =>
      c.transform(((_intro.value - a) / (b - a)).clamp(0.0, 1.0));

  @override
  Widget build(BuildContext context) {
    return AnnotatedRegion<SystemUiOverlayStyle>(
      value: SystemUiOverlayStyle.light.copyWith(
        statusBarColor: Colors.transparent,
        systemNavigationBarColor: AppColors.night,
      ),
      child: Scaffold(
        backgroundColor: AppColors.night,
        body: AnimatedBuilder(
          animation: Listenable.merge([_intro, _ambient]),
          builder: (context, _) {
            final size = MediaQuery.sizeOf(context);
            final mark = math.min(size.width * 0.42, 176.0);
            final text = _seg(0.55, 0.95);
            final sub = _seg(0.68, 1.0);
            return Stack(children: [
              // Soft green aurora behind the mark.
              Positioned.fill(
                child: CustomPaint(painter: _AuroraPainter(glow: _seg(0.0, 0.8, Curves.easeOut), t: _ambient.value)),
              ),
              Align(
                alignment: const Alignment(0, -0.12),
                child: Column(mainAxisSize: MainAxisSize.min, children: [
                  SizedBox.square(
                    dimension: mark,
                    child: CustomPaint(
                      painter: _OrbitMarkPainter(
                        ring: _seg(0.0, 0.55, Curves.easeInOutCubic),
                        travel: _seg(0.12, 0.72, Curves.easeInOutCubic),
                        land: _seg(0.66, 0.86, Curves.elasticOut),
                      ),
                    ),
                  ),
                  const SizedBox(height: 26),
                  Opacity(
                    opacity: text,
                    child: Transform.translate(
                      offset: Offset(0, 14 * (1 - text)),
                      child: Text(
                        AppConfig.appName,
                        style: TextStyle(
                          color: const Color(0xFFF4F3EF),
                          fontSize: 34,
                          fontWeight: FontWeight.w800,
                          letterSpacing: 10 + 6 * (1 - text),
                        ),
                      ),
                    ),
                  ),
                  const SizedBox(height: 10),
                  Opacity(
                    opacity: sub,
                    child: Transform.translate(
                      offset: Offset(0, 10 * (1 - sub)),
                      child: Text(tr(AppConfig.slogan),
                          style: TextStyle(color: Color(0xFF9C9B97), fontSize: 14, letterSpacing: 3)),
                    ),
                  ),
                ]),
              ),
              Positioned(
                left: 28,
                right: 28,
                bottom: 0,
                child: SafeArea(
                  top: false,
                  minimum: const EdgeInsets.only(bottom: 30),
                  child: AnimatedSwitcher(
                    duration: const Duration(milliseconds: 300),
                    child: widget.failed
                        ? _RetryBlock(key: const ValueKey('retry'), message: widget.message, onRetry: widget.onRetry)
                        : Opacity(
                            key: const ValueKey('hint'),
                            opacity: sub,
                            child: Text(
                              _slow ? tr('正在连接服务器…') : tr('无需注册 · 打开即用'),
                              textAlign: TextAlign.center,
                              style: const TextStyle(color: Color(0xFF66656A), fontSize: 12.5, letterSpacing: 1),
                            ),
                          ),
                  ),
                ),
              ),
            ]);
          },
        ),
      ),
    );
  }
}

class _RetryBlock extends StatelessWidget {
  const _RetryBlock({super.key, required this.message, required this.onRetry});
  final String? message;
  final Future<void> Function() onRetry;

  @override
  Widget build(BuildContext context) {
    return Column(mainAxisSize: MainAxisSize.min, children: [
      Text(message ?? tr('暂时连不上服务器'),
          textAlign: TextAlign.center, style: const TextStyle(color: Color(0xFF9C9B97), fontSize: 13)),
      const SizedBox(height: 14),
      ClipRRect(
        borderRadius: BorderRadius.circular(16),
        child: BackdropFilter(
          filter: ImageFilter.blur(sigmaX: 18, sigmaY: 18),
          child: Material(
            color: Colors.white.withValues(alpha: 0.08),
            shape: RoundedRectangleBorder(
              borderRadius: BorderRadius.circular(16),
              side: BorderSide(color: Colors.white.withValues(alpha: 0.14)),
            ),
            child: InkWell(
              onTap: onRetry,
              child: SizedBox(
                width: double.infinity,
                height: 52,
                child: Center(
                  child: Text(tr('重试'), style: TextStyle(color: Colors.white, fontSize: 16, fontWeight: FontWeight.w600)),
                ),
              ),
            ),
          ),
        ),
      ),
    ]);
  }
}

/// The bare mark (no tile): white ring with a gap at the top-right and the
/// green dot that orbits once before settling in the gap.
class _OrbitMarkPainter extends CustomPainter {
  _OrbitMarkPainter({required this.ring, required this.travel, required this.land});
  final double ring; // 0..1 how much of the ring is drawn
  final double travel; // 0..1 dot orbit progress
  final double land; // 0..1(+overshoot) dot settle scale

  static const gapCenter = -math.pi / 4;
  static const gap = math.pi * 0.42;

  @override
  void paint(Canvas canvas, Size size) {
    final s = size.width;
    final c = Offset(s / 2, s / 2);
    final r = s * 0.25 * 1.6; // the bare mark is drawn larger than inside the icon tile
    final stroke = s * 0.095 * 1.6;

    // Faint full orbit track.
    canvas.drawCircle(
      c,
      r,
      Paint()
        ..style = PaintingStyle.stroke
        ..strokeWidth = 1
        ..color = Colors.white.withValues(alpha: 0.06 * ring),
    );

    // The ring sweeps in clockwise from the gap's trailing edge.
    final start = gapCenter + gap / 2;
    final sweep = (2 * math.pi - gap) * ring;
    if (sweep > 0.001) {
      canvas.drawArc(
        Rect.fromCircle(center: c, radius: r),
        start,
        sweep,
        false,
        Paint()
          ..style = PaintingStyle.stroke
          ..strokeWidth = stroke
          ..strokeCap = StrokeCap.round
          ..color = const Color(0xFFF4F3EF),
      );
    }

    // Dot: one lap of the orbit, ending in the gap.
    final angle = gapCenter + gap / 2 + (2 * math.pi - gap / 2) * travel;
    final p = c + Offset(math.cos(angle), math.sin(angle)) * r;
    final dotR = s * 0.075 * 1.6;
    final scale = travel < 1 ? 0.55 + 0.25 * travel : 0.8 + 0.2 * land;
    final opacity = (travel * 4).clamp(0.0, 1.0);
    canvas.drawCircle(
      p,
      dotR * scale * 2.4,
      Paint()
        ..color = AppColors.signal.withValues(alpha: 0.22 * opacity)
        ..maskFilter = const MaskFilter.blur(BlurStyle.normal, 14),
    );
    canvas.drawCircle(p, dotR * scale, Paint()..color = AppColors.signal.withValues(alpha: opacity));
  }

  @override
  bool shouldRepaint(_OrbitMarkPainter old) => old.ring != ring || old.travel != travel || old.land != land;
}

/// Slow-drifting green and blue glows on the night background.
class _AuroraPainter extends CustomPainter {
  _AuroraPainter({required this.glow, required this.t});
  final double glow;
  final double t;

  @override
  void paint(Canvas canvas, Size size) {
    final a = t * 2 * math.pi;
    void blob(Offset center, double radius, Color color) {
      canvas.drawCircle(
        center,
        radius,
        Paint()
          ..shader = RadialGradient(colors: [color, color.withValues(alpha: 0)])
              .createShader(Rect.fromCircle(center: center, radius: radius)),
      );
    }

    blob(
      Offset(size.width * (0.72 + 0.06 * math.cos(a)), size.height * (0.30 + 0.04 * math.sin(a))),
      size.width * 0.75,
      const Color(0xFF12A150).withValues(alpha: 0.20 * glow),
    );
    blob(
      Offset(size.width * (0.18 + 0.05 * math.sin(a)), size.height * (0.72 + 0.04 * math.cos(a))),
      size.width * 0.8,
      const Color(0xFF2B4A7A).withValues(alpha: 0.22 * glow),
    );
  }

  @override
  bool shouldRepaint(_AuroraPainter old) => old.glow != glow || old.t != t;
}
