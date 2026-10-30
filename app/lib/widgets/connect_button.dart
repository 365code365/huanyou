import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../core/vpn.dart';
import '../theme/app_theme.dart';
import '../i18n/i18n.dart';

/// The main power control.
///
/// Idle: a white disc with a hairline track and an ink power glyph.
/// Connecting: an ink arc sweeps around the track.
/// Connected: the disc fills with signal green and breathes softly.
class ConnectButton extends StatefulWidget {
  const ConnectButton({super.key, required this.status, required this.onTap, this.size = 200});

  final VpnStatus status;
  final VoidCallback? onTap;
  final double size;

  @override
  State<ConnectButton> createState() => _ConnectButtonState();
}

class _ConnectButtonState extends State<ConnectButton> with TickerProviderStateMixin {
  late final AnimationController _spin =
      AnimationController(vsync: this, duration: const Duration(milliseconds: 1100));
  late final AnimationController _breath =
      AnimationController(vsync: this, duration: const Duration(milliseconds: 2600));
  bool _pressed = false;

  bool get _busy => widget.status == VpnStatus.connecting || widget.status == VpnStatus.disconnecting;
  bool get _on => widget.status == VpnStatus.connected;

  @override
  void initState() {
    super.initState();
    _sync();
  }

  @override
  void didUpdateWidget(covariant ConnectButton old) {
    super.didUpdateWidget(old);
    _sync();
  }

  void _sync() {
    _busy ? (_spin.isAnimating ? null : _spin.repeat()) : _spin.stop();
    if (_on) {
      if (!_breath.isAnimating) _breath.repeat(reverse: true);
    } else {
      _breath.stop();
      _breath.value = 0;
    }
  }

  @override
  void dispose() {
    _spin.dispose();
    _breath.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final s = widget.size;
    final outer = s * 1.3;
    return Semantics(
      button: true,
      label: _on ? tr('断开') : tr('连接'),
      child: GestureDetector(
        onTapDown: (_) => setState(() => _pressed = true),
        onTapCancel: () => setState(() => _pressed = false),
        onTapUp: (_) => setState(() => _pressed = false),
        onTap: widget.onTap,
        child: AnimatedScale(
          scale: _pressed ? 0.96 : 1,
          duration: const Duration(milliseconds: 120),
          child: SizedBox.square(
            dimension: outer,
            child: AnimatedBuilder(
              animation: Listenable.merge([_spin, _breath]),
              builder: (context, _) {
                final b = Curves.easeInOut.transform(_breath.value);
                return Stack(alignment: Alignment.center, children: [
                  // Soft halo when connected.
                  if (_on)
                    Container(
                      width: s * (1.14 + 0.08 * b),
                      height: s * (1.14 + 0.08 * b),
                      decoration: BoxDecoration(
                        shape: BoxShape.circle,
                        color: AppColors.signal.withValues(alpha: 0.10 - 0.04 * b),
                      ),
                    ),
                  // Track + sweeping arc.
                  CustomPaint(
                    size: Size.square(s * 1.14),
                    painter: _TrackPainter(
                      sweep: _busy ? _spin.value : null,
                      color: _on ? Colors.transparent : AppColors.line,
                    ),
                  ),
                  AnimatedContainer(
                    duration: const Duration(milliseconds: 380),
                    curve: Curves.easeOutCubic,
                    width: s,
                    height: s,
                    decoration: BoxDecoration(
                      shape: BoxShape.circle,
                      color: _on ? AppColors.signal : AppColors.surface,
                      border: Border.all(color: _on ? AppColors.signal : AppColors.line),
                      boxShadow: [
                        BoxShadow(
                          color: (_on ? AppColors.signal : AppColors.shadow).withValues(alpha: _on ? 0.30 : 0.07),
                          blurRadius: _on ? 36 : 28,
                          offset: const Offset(0, 14),
                        ),
                      ],
                    ),
                    child: Center(
                      child: Icon(
                        Icons.power_settings_new_rounded,
                        size: s * 0.34,
                        color: _on ? Colors.white : (_busy ? AppColors.faint : AppColors.ink),
                      ),
                    ),
                  ),
                ]);
              },
            ),
          ),
        ),
      ),
    );
  }
}

class _TrackPainter extends CustomPainter {
  _TrackPainter({required this.sweep, required this.color});
  final double? sweep; // 0..1 rotation while busy, null when idle
  final Color color;

  @override
  void paint(Canvas canvas, Size size) {
    final r = size.width / 2 - 1.5;
    final c = size.center(Offset.zero);
    canvas.drawCircle(
      c,
      r,
      Paint()
        ..style = PaintingStyle.stroke
        ..strokeWidth = 1.5
        ..color = color,
    );
    if (sweep != null) {
      final start = sweep! * 2 * math.pi - math.pi / 2;
      canvas.drawArc(
        Rect.fromCircle(center: c, radius: r),
        start,
        math.pi * 0.55,
        false,
        Paint()
          ..style = PaintingStyle.stroke
          ..strokeWidth = 3
          ..strokeCap = StrokeCap.round
          ..color = AppColors.ink,
      );
    }
  }

  @override
  bool shouldRepaint(_TrackPainter old) => old.sweep != sweep || old.color != color;
}
