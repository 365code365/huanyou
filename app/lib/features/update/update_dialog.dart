import 'package:flutter/material.dart';

import '../../core/api.dart';
import '../../core/format.dart';
import '../../core/updater.dart';
import '../../theme/app_theme.dart';

/// Shows the update dialog. A forced update can't be dismissed.
Future<void> showUpdateDialog(BuildContext context, AppUpdate u) => showDialog<void>(
      context: context,
      barrierDismissible: !u.force,
      builder: (_) => PopScope(canPop: !u.force, child: _UpdateDialog(update: u)),
    );

class _UpdateDialog extends StatefulWidget {
  const _UpdateDialog({required this.update});
  final AppUpdate update;

  @override
  State<_UpdateDialog> createState() => _UpdateDialogState();
}

class _UpdateDialogState extends State<_UpdateDialog> {
  double? _progress; // null = not downloading
  String? _error;

  Future<void> _install() async {
    setState(() {
      _progress = 0;
      _error = null;
    });
    try {
      await Updater.install(widget.update, onProgress: (p) {
        if (mounted) setState(() => _progress = p);
      });
      if (mounted && !widget.update.force) Navigator.pop(context);
    } on UpdateException catch (e) {
      if (mounted) setState(() => _error = e.message);
    } finally {
      if (mounted) setState(() => _progress = null);
    }
  }

  @override
  Widget build(BuildContext context) {
    final u = widget.update;
    final notes = u.notes.split('\n').map((l) => l.trim()).where((l) => l.isNotEmpty).toList();
    final busy = _progress != null;
    return AlertDialog(
      title: Row(children: [
        const Icon(Icons.system_update_rounded, color: AppColors.signal),
        const SizedBox(width: 8),
        Text('发现新版本 ${u.version}'),
      ]),
      content: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.start, children: [
        if (u.force)
          const Padding(
            padding: EdgeInsets.only(bottom: 10),
            child: Text('当前版本已停止支持，请更新后继续使用', style: TextStyle(color: AppColors.danger, fontSize: 13)),
          ),
        if (notes.isEmpty) const Text('优化体验，修复已知问题', style: TextStyle(color: AppColors.sub)),
        for (final n in notes)
          Padding(
            padding: const EdgeInsets.only(bottom: 4),
            child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
              const Text('·  ', style: TextStyle(color: AppColors.sub)),
              Expanded(child: Text(n, style: const TextStyle(fontSize: 14, height: 1.45))),
            ]),
          ),
        if (u.size > 0)
          Padding(
            padding: const EdgeInsets.only(top: 8),
            child: Text('安装包 ${formatBytes(u.size)}', style: const TextStyle(color: AppColors.faint, fontSize: 12)),
          ),
        if (busy) ...[
          const SizedBox(height: 14),
          ClipRRect(
            borderRadius: BorderRadius.circular(3),
            child: LinearProgressIndicator(value: _progress == 0 ? null : _progress, minHeight: 5, color: AppColors.signal),
          ),
          const SizedBox(height: 6),
          Text('下载中 ${((_progress ?? 0) * 100).round()}%', style: const TextStyle(fontSize: 12, color: AppColors.sub)),
        ],
        if (_error != null)
          Padding(
            padding: const EdgeInsets.only(top: 10),
            child: Text(_error!, style: const TextStyle(color: AppColors.danger, fontSize: 13)),
          ),
      ]),
      actions: [
        if (!u.force) TextButton(onPressed: busy ? null : () => Navigator.pop(context), child: const Text('稍后')),
        FilledButton(
          key: const Key('update-now'),
          onPressed: busy ? null : _install,
          child: Text(_error != null ? '重试' : '立即更新'),
        ),
      ],
    );
  }
}
