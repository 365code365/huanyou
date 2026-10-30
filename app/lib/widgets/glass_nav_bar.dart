import 'dart:math' as math;
import 'dart:ui';

import 'package:flutter/material.dart';
import 'package:flutter/physics.dart';
import 'package:flutter/services.dart';

import '../theme/app_theme.dart';

class GlassNavItem {
  const GlassNavItem({required this.label, required this.icon, required this.activeIcon});
  final String label;
  final IconData icon;
  final IconData activeIcon;
}

/// A floating "liquid glass" tab bar.
///
/// - The capsule blurs and saturates whatever scrolls behind it, with a light
///   rim that is brighter at the top edge (as if lit from above).
/// - The selection lens is a second glass layer that moves on a spring and
///   stretches with its speed, so it reads as liquid rather than a sliding box.
/// - The lens can be dragged across the bar; releasing it selects the tab under it.
///
/// Use inside a Scaffold with `extendBody: true` so content shows through.
class GlassNavBar extends StatefulWidget {
  const GlassNavBar({super.key, required this.items, required this.index, required this.onChanged});

  final List<GlassNavItem> items;
  final int index;
  final ValueChanged<int> onChanged;

  static const height = 64.0;

  @override
  State<GlassNavBar> createState() => _GlassNavBarState();
}

class _GlassNavBarState extends State<GlassNavBar> with SingleTickerProviderStateMixin {
  // Lens position in "tab units" (0 = first tab). Unbounded so the spring can overshoot.
  late final AnimationController _pos = AnimationController.unbounded(vsync: this, value: widget.index.toDouble());
  bool _dragging = false;
  double _dragVelocity = 0;

  static const _spring = SpringDescription(mass: 1, stiffness: 420, damping: 30);

  @override
  void didUpdateWidget(covariant GlassNavBar old) {
    super.didUpdateWidget(old);
    if (old.index != widget.index && !_dragging) _animateTo(widget.index);
  }

  void _animateTo(int i, [double velocity = 0]) {
    _pos.animateWith(SpringSimulation(_spring, _pos.value, i.toDouble(), velocity));
  }

  @override
  void dispose() {
    _pos.dispose();
    super.dispose();
  }

  void _select(int i) {
    if (i != widget.index) HapticFeedback.selectionClick();
    widget.onChanged(i);
    _animateTo(i);
  }

  @override
  Widget build(BuildContext context) {
    final bottom = MediaQuery.paddingOf(context).bottom;
    final n = widget.items.length;
    return Padding(
      padding: EdgeInsets.fromLTRB(22, 0, 22, math.max(bottom, 10) + 8),
      child: SizedBox(
        height: GlassNavBar.height,
        child: LayoutBuilder(builder: (context, c) {
          const inset = 6.0;
          final slot = (c.maxWidth - inset * 2) / n;
          return GestureDetector(
            behavior: HitTestBehavior.opaque,
            onHorizontalDragStart: (_) {
              _dragging = true;
              _pos.stop();
            },
            onHorizontalDragUpdate: (d) {
              _dragVelocity = d.delta.dx;
              _pos.value = (_pos.value + d.delta.dx / slot).clamp(-0.25, n - 0.75);
            },
            // A cancelled drag (e.g. the gesture arena hands it to a scroll view)
            // must still settle the lens on a tab, or it stays between two.
            onHorizontalDragCancel: () {
              if (!_dragging) return;
              _dragging = false;
              final target = _pos.value.round().clamp(0, n - 1);
              if (target != widget.index) widget.onChanged(target);
              _animateTo(target);
            },
            onHorizontalDragEnd: (d) {
              _dragging = false;
              final v = d.velocity.pixelsPerSecond.dx / slot;
              final target = (_pos.value + v * 0.12).round().clamp(0, n - 1);
              if (target != widget.index) HapticFeedback.selectionClick();
              widget.onChanged(target);
              _animateTo(target, v);
            },
            child: AnimatedBuilder(
              animation: _pos,
              builder: (context, _) {
                final v = _dragging ? _dragVelocity / 8 : _pos.velocity / 6;
                final stretch = (v.abs()).clamp(0.0, 1.0);
                final lensW = slot * (1 + 0.28 * stretch);
                final lensH = GlassNavBar.height - inset * 2 - 6 * stretch;
                final left = inset + _pos.value * slot + (slot - lensW) / 2;
                final focus = _pos.value;
                return Stack(clipBehavior: Clip.none, children: [
                  const Positioned.fill(child: _GlassCapsule()),
                  Positioned(
                    left: left,
                    top: (GlassNavBar.height - lensH) / 2,
                    width: lensW,
                    height: lensH,
                    child: _Lens(pressed: _dragging),
                  ),
                  Positioned.fill(
                    left: inset,
                    right: inset,
                    child: Row(children: [
                      for (var i = 0; i < n; i++)
                        Expanded(
                          child: _Tab(
                            key: Key('tab-${widget.items[i].label}'),
                            item: widget.items[i],
                            // 1 when the lens is centered on this tab, fading as it moves away.
                            focus: (1 - (focus - i).abs()).clamp(0.0, 1.0),
                            onTap: () => _select(i),
                          ),
                        ),
                    ]),
                  ),
                ]);
              },
            ),
          );
        }),
      ),
    );
  }
}

class _GlassCapsule extends StatelessWidget {
  const _GlassCapsule();

  // Boost saturation of what's behind, like real glass refracting colour.
  static final _saturate = ColorFilter.matrix(_saturation(1.7));

  static List<double> _saturation(double s) {
    const lr = 0.2126, lg = 0.7152, lb = 0.0722;
    final sr = (1 - s) * lr, sg = (1 - s) * lg, sb = (1 - s) * lb;
    return [
      sr + s, sg, sb, 0, 0, //
      sr, sg + s, sb, 0, 0,
      sr, sg, sb + s, 0, 0,
      0, 0, 0, 1, 0,
    ];
  }

  @override
  Widget build(BuildContext context) {
    final radius = BorderRadius.circular(GlassNavBar.height / 2);
    return DecoratedBox(
      decoration: BoxDecoration(
        borderRadius: radius,
        boxShadow: [
          BoxShadow(color: AppColors.ink.withValues(alpha: 0.10), blurRadius: 30, offset: const Offset(0, 12)),
          BoxShadow(color: AppColors.ink.withValues(alpha: 0.05), blurRadius: 6, offset: const Offset(0, 2)),
        ],
      ),
      child: ClipRRect(
        borderRadius: radius,
        child: BackdropFilter(
          filter: ImageFilter.compose(outer: _saturate, inner: ImageFilter.blur(sigmaX: 22, sigmaY: 22)),
          child: CustomPaint(
            painter: _GlassPainter(radius: GlassNavBar.height / 2, strong: true),
            child: const SizedBox.expand(),
          ),
        ),
      ),
    );
  }
}

class _Lens extends StatelessWidget {
  const _Lens({required this.pressed});
  final bool pressed;

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(builder: (context, c) {
      final r = c.maxHeight / 2;
      return AnimatedScale(
        scale: pressed ? 1.06 : 1,
        duration: const Duration(milliseconds: 160),
        child: DecoratedBox(
          decoration: BoxDecoration(
            borderRadius: BorderRadius.circular(r),
            boxShadow: [
              BoxShadow(color: AppColors.ink.withValues(alpha: pressed ? 0.14 : 0.08), blurRadius: 14, offset: const Offset(0, 4)),
            ],
          ),
          child: ClipRRect(
            borderRadius: BorderRadius.circular(r),
            child: BackdropFilter(
              filter: ImageFilter.blur(sigmaX: 6, sigmaY: 6),
              child: CustomPaint(painter: _GlassPainter(radius: r, strong: false), child: const SizedBox.expand()),
            ),
          ),
        ),
      );
    });
  }
}

/// Fill + rim of a glass surface. The rim is a gradient stroke: bright along
/// the top edge, fading towards the bottom, with a faint bottom bounce light.
class _GlassPainter extends CustomPainter {
  _GlassPainter({required this.radius, required this.strong});
  final double radius;
  final bool strong; // capsule (true) or lens (false)

  @override
  void paint(Canvas canvas, Size size) {
    final rect = Offset.zero & size;
    final rr = RRect.fromRectAndRadius(rect, Radius.circular(radius));

    // Body: milky white, lighter at the top.
    canvas.drawRRect(
      rr,
      Paint()
        ..shader = LinearGradient(
          begin: Alignment.topCenter,
          end: Alignment.bottomCenter,
          colors: strong
              ? [Colors.white.withValues(alpha: 0.72), Colors.white.withValues(alpha: 0.48)]
              : [Colors.white.withValues(alpha: 0.85), const Color(0xFFF1F0EC).withValues(alpha: 0.75)],
        ).createShader(rect),
    );

    // Specular sheen across the upper half.
    final sheen = Rect.fromLTWH(rect.left + radius * 0.5, rect.top + 1.5, rect.width - radius, rect.height * 0.45);
    canvas.drawRRect(
      RRect.fromRectAndRadius(sheen, Radius.circular(radius)),
      Paint()
        ..shader = LinearGradient(
          begin: Alignment.topCenter,
          end: Alignment.bottomCenter,
          colors: [Colors.white.withValues(alpha: strong ? 0.55 : 0.7), Colors.white.withValues(alpha: 0)],
        ).createShader(sheen),
    );

    // Rim.
    canvas.drawRRect(
      rr.deflate(0.5),
      Paint()
        ..style = PaintingStyle.stroke
        ..strokeWidth = 1
        ..shader = LinearGradient(
          begin: Alignment.topCenter,
          end: Alignment.bottomCenter,
          colors: [
            Colors.white.withValues(alpha: 0.95),
            Colors.white.withValues(alpha: 0.25),
            AppColors.ink.withValues(alpha: strong ? 0.06 : 0.08),
          ],
          stops: const [0, 0.55, 1],
        ).createShader(rect),
    );
  }

  @override
  bool shouldRepaint(_GlassPainter old) => old.radius != radius || old.strong != strong;
}

class _Tab extends StatelessWidget {
  const _Tab({super.key, required this.item, required this.focus, required this.onTap});
  final GlassNavItem item;
  final double focus; // 0..1
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final color = Color.lerp(AppColors.faint, AppColors.ink, focus)!;
    return Semantics(
      button: true,
      selected: focus > 0.5,
      label: item.label,
      child: InkResponse(
        onTap: onTap,
        radius: 34,
        highlightColor: Colors.transparent,
        splashColor: AppColors.ink.withValues(alpha: 0.05),
        child: Column(mainAxisAlignment: MainAxisAlignment.center, children: [
          Transform.scale(
            scale: 1 + 0.08 * focus,
            child: Icon(focus > 0.5 ? item.activeIcon : item.icon, size: 23, color: color),
          ),
          const SizedBox(height: 2),
          Text(item.label,
              style: TextStyle(
                fontSize: 11,
                height: 1.1,
                color: color,
                fontWeight: focus > 0.5 ? FontWeight.w700 : FontWeight.w500,
              )),
        ]),
      ),
    );
  }
}
