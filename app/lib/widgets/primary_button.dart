import 'package:flutter/material.dart';

import '../theme/app_theme.dart';

/// Full-width solid ink button with a loading state.
class PrimaryButton extends StatelessWidget {
  const PrimaryButton({super.key, required this.label, required this.onPressed, this.loading = false, this.icon});

  final String label;
  final VoidCallback? onPressed;
  final bool loading;
  final IconData? icon;

  @override
  Widget build(BuildContext context) {
    final enabled = onPressed != null && !loading;
    return SizedBox(
      height: 52,
      child: FilledButton(
        style: FilledButton.styleFrom(
          backgroundColor: AppColors.ink,
          disabledBackgroundColor: AppColors.ink.withValues(alpha: loading ? 1 : 0.35),
          foregroundColor: AppColors.onInk,
          disabledForegroundColor: AppColors.onInk,
          shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(AppRadius.button)),
        ),
        onPressed: enabled ? onPressed : null,
        child: loading
            ? SizedBox.square(dimension: 20, child: CircularProgressIndicator(strokeWidth: 2.2, color: AppColors.onInk))
            : Row(mainAxisSize: MainAxisSize.min, children: [
                if (icon != null) ...[Icon(icon, size: 19), const SizedBox(width: 8)],
                Flexible(
                  child: Text(label,
                      maxLines: 1, overflow: TextOverflow.ellipsis, style: const TextStyle(fontSize: 15, fontWeight: FontWeight.w600)),
                ),
              ]),
      ),
    );
  }
}
