import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../core/analytics.dart';
import '../../core/api.dart';
import '../../core/background.dart';
import '../../core/config.dart';
import '../../core/format.dart';
import '../../core/open_url.dart';
import '../../core/prefs.dart';
import '../../core/updater.dart';
import '../../i18n/i18n.dart';
import '../../state/app_scope.dart';
import '../../theme/app_theme.dart';
import '../../theme/appearance.dart';
import '../../widgets/brand_mark.dart';
import '../../widgets/section_card.dart';
import '../update/update_dialog.dart';
import 'background_sheet.dart';
import 'preferences_sheets.dart';

/// 我的 → 设置: background running (Android), language, appearance, updates,
/// crash reports, cache, about.
class SettingsPage extends StatefulWidget {
  const SettingsPage({super.key});
  @override
  State<SettingsPage> createState() => _SettingsPageState();
}

class _SettingsPageState extends State<SettingsPage> {
  static const _device = MethodChannel('vpn_platform/device');
  String _version = '';
  int? _cacheBytes;
  bool _checking = false;
  bool _crash = Analytics.crashReportsOn;
  BackgroundState? _background; // Android only

  @override
  void initState() {
    super.initState();
    Analytics.track('settings_view');
    Updater.info().then((i) {
      if (mounted) setState(() => _version = '${i.version} (${Updater.buildOf(i)})');
    });
    _measureCache();
    _loadBackground();
  }

  Future<void> _loadBackground() async {
    final s = await BackgroundRun.state();
    if (mounted) setState(() => _background = s);
  }

  Future<void> _measureCache() async {
    try {
      final n = await _device.invokeMethod<int>('cacheSize') ?? 0;
      final img = PaintingBinding.instance.imageCache.currentSizeBytes;
      if (mounted) setState(() => _cacheBytes = n + img);
    } catch (_) {
      if (mounted) setState(() => _cacheBytes = PaintingBinding.instance.imageCache.currentSizeBytes);
    }
  }

  Future<void> _clearCache() async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: Text(tr('清除缓存')),
        content: Text(tr('清除图片和临时文件。账号、语言和连接设置不受影响。')),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: Text(tr('取消'))),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: Text(tr('清除'))),
        ],
      ),
    );
    if (ok != true) return;
    Analytics.track('cache_clear');
    PaintingBinding.instance.imageCache
      ..clear()
      ..clearLiveImages();
    try {
      await _device.invokeMethod<int>('clearCache');
    } catch (_) {}
    // Cached connection profile: re-fetched on the next connect.
    await Prefs.remove('profile');
    await _measureCache();
    if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(tr('缓存已清除'))));
  }

  Future<void> _checkUpdate() async {
    setState(() => _checking = true);
    try {
      final u = await Updater.check(AppScope.read(context).api);
      if (!mounted) return;
      if (u != null && u.hasUpdate) {
        await showUpdateDialog(context, u);
      } else {
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(tr('已是最新版本'))));
      }
    } catch (e) {
      final msg = e is ApiException ? e.message : tr('检查失败，请稍后重试');
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(msg)));
    } finally {
      if (mounted) setState(() => _checking = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    // Rebuild when language / appearance change from the sheets.
    return ListenableBuilder(
      listenable: Listenable.merge([I18n.instance, Appearance.instance]),
      builder: (context, _) => Scaffold(
        appBar: AppBar(title: Text(tr('设置'))),
        body: ListView(padding: const EdgeInsets.fromLTRB(18, 4, 18, 32), children: [
          // Android: keep the VPN from being closed by the phone in the background.
          if (_background != null)
            _Group(label: tr('连接'), children: [
              MenuRow(
                key: const Key('settings-background'),
                icon: Icons.battery_charging_full_rounded,
                title: tr('后台运行'),
                value: _background!.ignoringBattery ? null : tr('建议设置'),
                onTap: () async {
                  await showBackgroundGuide(context);
                  await _loadBackground();
                },
              ),
            ]),
          _Group(label: tr('通用'), children: [
            MenuRow(
              key: const Key('settings-language'),
              icon: Icons.translate_rounded,
              title: tr('语言'),
              value: languageLabel(),
              onTap: () => showLanguageSheet(context),
            ),
            MenuRow(
              key: const Key('settings-appearance'),
              icon: Icons.contrast_rounded,
              title: tr('外观'),
              value: tr(Appearance.instance.mode.label),
              onTap: () => showAppearanceSheet(context),
            ),
          ]),
          _Group(label: tr('数据与存储'), children: [
            _SwitchRow(
              icon: Icons.bug_report_outlined,
              title: tr('回传崩溃日志'),
              subtitle: tr('App 出错时自动发送错误信息，帮助我们修复问题，不包含浏览内容'),
              value: _crash,
              onChanged: (v) {
                setState(() => _crash = v);
                Analytics.setCrashReports(v);
              },
            ),
            MenuRow(
              key: const Key('settings-cache'),
              icon: Icons.cleaning_services_outlined,
              title: tr('清除缓存'),
              value: _cacheBytes == null ? null : formatBytes(_cacheBytes!),
              onTap: _clearCache,
            ),
          ]),
          _Group(label: tr('关于'), children: [
            // App Store builds are updated by the App Store, never in-app.
            if (!AppScope.of(context).iapFace)
            MenuRow(
              key: const Key('settings-update'),
              icon: Icons.system_update_rounded,
              color: AppColors.signal,
              title: tr('检查更新'),
              value: _checking ? tr('检查中…') : (_version.isEmpty ? null : 'v$_version'),
              onTap: _checking ? null : _checkUpdate,
            ),
            MenuRow(
              icon: Icons.info_outline_rounded,
              title: tr('关于{0}', [AppConfig.appName]),
              onTap: () => Navigator.push(context, MaterialPageRoute(builder: (_) => _AboutPage(version: _version))),
            ),
          ]),
        ]),
      ),
    );
  }
}

class _Group extends StatelessWidget {
  const _Group({required this.label, required this.children});
  final String label;
  final List<Widget> children;
  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.only(top: 14),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(6, 0, 6, 8),
          child: Text(label, style: AppText.caption.copyWith(color: AppColors.sub, fontWeight: FontWeight.w600)),
        ),
        Card(
          clipBehavior: Clip.antiAlias,
          child: Column(children: [
            for (var i = 0; i < children.length; i++) ...[
              if (i > 0) const Divider(indent: 66),
              children[i],
            ],
          ]),
        ),
      ]),
    );
  }
}

class _SwitchRow extends StatelessWidget {
  const _SwitchRow({required this.icon, required this.title, required this.subtitle, required this.value, required this.onChanged});
  final IconData icon;
  final String title, subtitle;
  final bool value;
  final ValueChanged<bool> onChanged;
  @override
  Widget build(BuildContext context) {
    return InkWell(
      onTap: () => onChanged(!value),
      child: Padding(
        padding: const EdgeInsets.fromLTRB(18, 12, 12, 12),
        child: Row(children: [
          Container(
            width: 34,
            height: 34,
            decoration: BoxDecoration(color: AppColors.primary.withValues(alpha: 0.1), borderRadius: BorderRadius.circular(10)),
            child: Icon(icon, size: 19, color: AppColors.primary),
          ),
          const SizedBox(width: 14),
          Expanded(
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text(title, style: AppText.subtitle),
              const SizedBox(height: 2),
              Text(subtitle, style: AppText.caption.copyWith(color: AppColors.sub)),
            ]),
          ),
          Switch(value: value, onChanged: onChanged),
        ]),
      ),
    );
  }
}

class _AboutPage extends StatelessWidget {
  const _AboutPage({required this.version});
  final String version;
  @override
  Widget build(BuildContext context) {
    // App Store face: no links to downloads outside the App Store.
    final base = AppConfig.apiBaseUrls.isEmpty || AppScope.of(context).iapFace ? null : AppConfig.apiBaseUrls.first;
    return Scaffold(
      appBar: AppBar(title: Text(tr('关于{0}', [AppConfig.appName]))),
      body: ListView(padding: const EdgeInsets.fromLTRB(24, 24, 24, 32), children: [
        const Center(child: BrandMark(size: 76)),
        const SizedBox(height: 14),
        Center(child: Text(AppConfig.appName, style: AppText.headline)),
        const SizedBox(height: 4),
        Center(child: Text(tr(AppConfig.slogan), style: AppText.bodySmall.copyWith(color: AppColors.sub))),
        const SizedBox(height: 4),
        Center(child: Text(version.isEmpty ? '' : 'v$version', style: AppText.caption.copyWith(color: AppColors.faint))),
        const SizedBox(height: 28),
        Card(
          child: Padding(
            padding: const EdgeInsets.all(18),
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text(tr('隐私说明'), style: AppText.subtitle),
              const SizedBox(height: 8),
              Text(
                tr('我们不记录你访问的网站和内容。为了保障服务，会记录连接来源 IP 和使用次数等统计数据；崩溃日志可在设置中关闭。'),
                style: AppText.bodySmall.copyWith(color: AppColors.sub),
              ),
            ]),
          ),
        ),
        if (base != null) ...[
          const SizedBox(height: 12),
          Card(
            clipBehavior: Clip.antiAlias,
            child: MenuRow(
              icon: Icons.public_rounded,
              title: tr('官网 / 下载页'),
              onTap: () => openUrl(base.replace(path: '/download')),
            ),
          ),
        ],
        // Not on iOS: App Review rejects builds that call themselves a test /
        // invite-only version.
        if (!Platform.isIOS) ...[
          const SizedBox(height: 24),
          Center(child: Text(tr('内部测试版，仅限受邀用户使用'), style: AppText.caption.copyWith(color: AppColors.faint))),
        ],
      ]),
    );
  }
}
