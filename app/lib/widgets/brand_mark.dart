import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../theme/app_theme.dart';

/// The app's mark: an ink tile holding an open white ring, with a green dot
/// sitting in the ring's gap. Same geometry as the launcher icon
/// (scripts/make_icon.py), so the in-app mark and the icon match.
class BrandMark extends StatelessWidget {
  const BrandMark({super.key, this.size = 32, this.inverted = false});
  final double size;

  /// White tile with an ink ring (for dark backgrounds).
  final bool inverted;

  @override
  Widget build(BuildContext context) {
    return CustomPaint(size: Size.square(size), painter: _MarkPainter(inverted));
  }
}

class _MarkPainter extends CustomPainter {
  _MarkPainter(this.inverted);
  final bool inverted;

  @override
  void paint(Canvas canvas, Size size) {
    final s = size.width;
    final tile = RRect.fromRectAndRadius(Offset.zero & size, Radius.circular(s * 0.27));
    canvas.drawRRect(tile, Paint()..color = inverted ? Colors.white : AppColors.ink);

    final c = Offset(s / 2, s / 2);
    final r = s * 0.25;
    const gapCenter = -math.pi / 4; // top-right
    const gap = math.pi * 0.42;
    canvas.drawArc(
      Rect.fromCircle(center: c, radius: r),
      gapCenter + gap / 2,
      2 * math.pi - gap,
      false,
      Paint()
        ..style = PaintingStyle.stroke
        ..strokeWidth = s * 0.095
        ..strokeCap = StrokeCap.round
        ..color = inverted ? AppColors.night : AppColors.onInk,
    );
    canvas.drawCircle(
      c + Offset(math.cos(gapCenter), math.sin(gapCenter)) * r,
      s * 0.075,
      Paint()..color = AppColors.signal,
    );
  }

  @override
  bool shouldRepaint(_MarkPainter old) => old.inverted != inverted;
}
