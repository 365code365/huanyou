import 'package:flutter/material.dart';

import '../../core/api.dart';
import '../../core/desktop_updater.dart';
import '../../core/format.dart';
import '../../core/updater.dart';
import '../../theme/app_theme.dart';
import '../../i18n/i18n.dart';
import '../../core/analytics.dart';

bool _updateDialogOpen = false;

/// Shows the update dialog (once at a time). A forced update can't be dismissed.
Future<void> showUpdateDialog(BuildContext context, AppUpdate u) async {
  if (_updateDialogOpen) return;
  _updateDialogOpen = true;
  try {
    await showDialog<void>(
      context: context,
      barrierDismissible: !u.force,
      builder: (_) => PopScope(canPop: !u.force, child: _UpdateDialog(update: u)),
    );
  } finally {
    _updateDialogOpen = false;
  }
}

class _UpdateDialog extends StatefulWidget {
  const _UpdateDialog({required this.update});
  final AppUpdate update;

  @override
  State<_UpdateDialog> createState() => _UpdateDialogState();
}

class _UpdateDialogState extends State<_UpdateDialog> {
  String? _error;
  bool _opened = false;

  /// Desktop: download, verify and install in the app ("" = possible).
  late final String _desktopBlocker = DesktopUpdater.supported ? DesktopUpdater.blocker() : 'n/a';
  bool get _inApp => _desktopBlocker.isEmpty;
  double? _progress; // downloading (0..1)
  bool _installing = false;

  Future<void> _update() async {
    if (!_inApp) return _install();
    setState(() {
      _error = null;
      _progress = 0;
    });
    try {
      Analytics.track('update_download');
      final pkg = await DesktopUpdater.download(widget.update, (p) {
        if (mounted) setState(() => _progress = p);
      });
      if (!mounted) return;
      setState(() => _installing = true);
      Analytics.track('update_install');
      await Analytics.flush();
      await DesktopUpdater.installAndRestart(pkg); // quits the app
    } on UpdateException catch (e) {
      if (mounted) setState(() => _error = e.message);
    } catch (e) {
      if (mounted) setState(() => _error = tr('更新失败：{0}', [e]));
    } finally {
      if (mounted) {
        setState(() {
          _progress = null;
          _installing = false;
        });
      }
    }
  }

  /// Opens the download in the system browser (the reliable install path).
  Future<void> _install() async {
    setState(() => _error = null);
    try {
      Analytics.track('update_open');
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
          child: Text(
              _inApp
                  ? tr('下载完成后自动安装并重新打开，期间会断开 VPN。')
                  : DesktopUpdater.supported
                      ? _desktopBlocker
                      : tr('将在浏览器中打开下载，下载完成后点击安装包即可安装。'),
              style: TextStyle(color: AppColors.sub, fontSize: 13, height: 1.5)),
        ),
        if (_progress != null)
          Padding(
            padding: const EdgeInsets.only(top: 12),
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              LinearProgressIndicator(value: _installing ? null : _progress, color: AppColors.signal, minHeight: 4),
              const SizedBox(height: 6),
              Text(
                _installing
                    ? tr('正在安装，App 将自动重新打开…')
                    : tr('正在下载 {0}%', [((_progress ?? 0) * 100).toStringAsFixed(0)]),
                style: TextStyle(color: AppColors.sub, fontSize: 12),
              ),
            ]),
          ),
        if (_opened)
          Padding(
            padding: EdgeInsets.only(top: 8),
            child: Text(tr('已打开浏览器。若没有开始下载，可再点一次。'), style: TextStyle(color: AppColors.signal, fontSize: 13)),
          ),
        if (_error != null)
          Padding(
            padding: const EdgeInsets.only(top: 10),
            child: Text(_error!, style: const TextStyle(color: AppColors.danger, fontSize: 13)),
          ),
      ]),
      actions: [
        if (_inApp && _error != null)
          TextButton(onPressed: _install, child: Text(tr('浏览器下载'))),
        if (!u.force && _progress == null) TextButton(onPressed: () => Navigator.pop(context), child: Text(tr('稍后'))),
        FilledButton(
          key: const Key('update-now'),
          onPressed: _progress != null ? null : _update,
          child: Text(_error != null ? tr('重试') : _inApp ? tr('立即更新') : tr('去浏览器下载')),
        ),
      ],
    );
  }
}
