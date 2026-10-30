import 'package:flutter/material.dart';

import '../theme/app_theme.dart';

/// White rounded card with optional title row. The standard container for content blocks.
class SectionCard extends StatelessWidget {
  const SectionCard({super.key, this.title, this.trailing, required this.child, this.padding, this.onTap});

  final String? title;
  final Widget? trailing;
  final Widget child;
  final EdgeInsetsGeometry? padding;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final body = Padding(
      padding: padding ?? const EdgeInsets.all(18),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, mainAxisSize: MainAxisSize.min, children: [
        if (title != null) ...[
          Row(children: [
            Text(title!, style: const TextStyle(fontSize: 15, fontWeight: FontWeight.w600)),
            const Spacer(),
            ?trailing,
          ]),
          const SizedBox(height: 12),
        ],
        child,
      ]),
    );
    return Card(
      clipBehavior: Clip.antiAlias,
      child: onTap == null ? body : InkWell(onTap: onTap, child: body),
    );
  }
}

/// A tappable settings-style row (icon, title, optional value, chevron).
class MenuRow extends StatelessWidget {
  const MenuRow({super.key, required this.icon, required this.title, this.value, this.onTap, this.color, this.danger = false});

  final IconData icon;
  final String title;
  final String? value;
  final VoidCallback? onTap;
  final Color? color;
  final bool danger;

  @override
  Widget build(BuildContext context) {
    final c = danger ? AppColors.danger : (color ?? AppColors.primary);
    return InkWell(
      onTap: onTap,
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 18, vertical: 15),
        child: Row(children: [
          Container(
            width: 34,
            height: 34,
            decoration: BoxDecoration(color: c.withValues(alpha: 0.1), borderRadius: BorderRadius.circular(10)),
            child: Icon(icon, size: 19, color: c),
          ),
          const SizedBox(width: 14),
          Expanded(
            child: Text(title,
                style: TextStyle(fontSize: 15, color: danger ? AppColors.danger : AppColors.ink)),
          ),
          if (value != null) Text(value!, style: const TextStyle(color: AppColors.sub, fontSize: 13)),
          if (!danger) const Icon(Icons.chevron_right_rounded, color: AppColors.faint),
        ]),
      ),
    );
  }
}
