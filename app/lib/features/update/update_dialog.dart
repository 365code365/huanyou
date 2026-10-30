import 'package:flutter/material.dart';

import '../../core/api.dart';
import '../../core/format.dart';
import '../../core/updater.dart';
import '../../theme/app_theme.dart';
import '../../i18n/i18n.dart';

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
  String? _error;
  bool _opened = false;

  /// Opens the download in the system browser (the reliable install path).
  Future<void> _install() async {
    setState(() => _error = null);
    try {
      await Updater.install(widget.update);
      if (!mounted) return;
      if (widget.update.force) {
        setState(() => _opened = true); // stay up until the new version is installed
      } else {
        Navigator.pop(context);
      }
    } on UpdateException catch (e) {
      if (mounted) setState(() => _error = e.message);
    }
  }

  @override
  Widget build(BuildContext context) {
    final u = widget.update;
    final notes = u.notes.split('\n').map((l) => l.trim()).where((l) => l.isNotEmpty).toList();
    return AlertDialog(
      title: Row(children: [
        const Icon(Icons.system_update_rounded, color: AppColors.signal),
        const SizedBox(width: 8),
        Expanded(child: Text(tr('发现新版本 {0}', [u.version]))),
      ]),
      content: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.start, children: [
        if (u.force)
          Padding(
            padding: EdgeInsets.only(bottom: 10),
            child: Text(tr('当前版本已停止支持，请更新后继续使用'), style: TextStyle(color: AppColors.danger, fontSize: 13)),
          ),
        if (notes.isEmpty) Text(tr('优化体验，修复已知问题'), style: TextStyle(color: AppColors.sub)),
        for (final n in notes)
          Padding(
            padding: const EdgeInsets.only(bottom: 4),
            child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text('·  ', style: TextStyle(color: AppColors.sub)),
              Expanded(child: Text(n, style: const TextStyle(fontSize: 14, height: 1.45))),
            ]),
          ),
        if (u.size > 0)
          Padding(
            padding: const EdgeInsets.only(top: 8),
            child: Text(tr('安装包 {0}', [formatBytes(u.size)]), style: TextStyle(color: AppColors.faint, fontSize: 12)),
          ),
        Padding(
          padding: EdgeInsets.only(top: 10),
          child: Text(tr('将在浏览器中打开下载，下载完成后点击安装包即可安装。'),
              style: TextStyle(color: AppColors.sub, fontSize: 12.5, height: 1.5)),
        ),
        if (_opened)
          Padding(
            padding: EdgeInsets.only(top: 8),
            child: Text(tr('已打开浏览器。若没有开始下载，可再点一次。'), style: TextStyle(color: AppColors.signal, fontSize: 12.5)),
          ),
        if (_error != null)
          Padding(
            padding: const EdgeInsets.only(top: 10),
            child: Text(_error!, style: const TextStyle(color: AppColors.danger, fontSize: 13)),
          ),
      ]),
      actions: [
        if (!u.force) TextButton(onPressed: () => Navigator.pop(context), child: Text(tr('稍后'))),
        FilledButton(
          key: const Key('update-now'),
          onPressed: _install,
          child: Text(_error != null ? tr('重试') : tr('去浏览器下载')),
        ),
      ],
    );
  }
}
