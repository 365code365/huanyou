import 'package:flutter/material.dart';

import '../theme/app_theme.dart';

/// Pulsing placeholder shown while remote content (banners, popup images,
/// announcements) is loading. Fills its parent; give it a size or wrap it.
class Skeleton extends StatefulWidget {
  const Skeleton({super.key, this.radius = 0});
  final double radius;

  @override
  State<Skeleton> createState() => _SkeletonState();
}

class _SkeletonState extends State<Skeleton> with SingleTickerProviderStateMixin {
  late final AnimationController _c =
      AnimationController(vsync: this, duration: const Duration(milliseconds: 900))..repeat(reverse: true);

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => ExcludeSemantics(
        child: AnimatedBuilder(
          animation: _c,
          builder: (_, _) => DecoratedBox(
            decoration: BoxDecoration(
              color: Color.lerp(AppColors.sunken, AppColors.line, _c.value),
              borderRadius: BorderRadius.circular(widget.radius),
            ),
            child: const SizedBox.expand(),
          ),
        ),
      );
}
