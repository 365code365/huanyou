import 'package:flutter/material.dart';

import '../../core/analytics.dart';
import '../../core/background.dart';
import '../../core/config.dart';
import '../../i18n/i18n.dart';
import '../../theme/app_theme.dart';

/// 保持后台运行 (Android): the settings that stop the phone from closing the
/// app, and the VPN with it, in the background. The steps follow the
/// phone's maker, since each brand names them differently.
Future<void> showBackgroundGuide(BuildContext context) {
  Analytics.track('background_guide_view');
  return showModalBottomSheet<void>(
    context: context,
    showDragHandle: true,
    isScrollControlled: true,
    builder: (_) => const _BackgroundGuide(),
  );
}

class _BackgroundGuide extends StatefulWidget {
  const _BackgroundGuide();
  @override
  State<_BackgroundGuide> createState() => _BackgroundGuideState();
}

class _BackgroundGuideState extends State<_BackgroundGuide> with WidgetsBindingObserver {
  BackgroundState? _state;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _load();
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  // Back from the system settings: show what changed.
  @override
  void didChangeAppLifecycleState(AppLifecycleState s) {
    if (s == AppLifecycleState.resumed) _load();
  }

  Future<void> _load() async {
    final s = await BackgroundRun.state();
    if (mounted) setState(() => _state = s);
  }

  @override
  Widget build(BuildContext context) {
    final s = _state;
    final sys = s?.system ?? PhoneSystem.other;
    final app = AppConfig.appName;
    final (settingsTitle, settingsBody) = switch (sys) {
      PhoneSystem.coloros => (
          tr('允许完全后台行为'),
          tr('在应用设置中打开「耗电管理」，选择「允许完全后台行为」，并打开「允许自启动」和「允许关联启动」。'),
        ),
      PhoneSystem.miui => (
          tr('省电策略设为无限制'),
          tr('在应用设置中把「省电策略」设为「无限制」，并打开「自启动」。'),
        ),
      PhoneSystem.huawei => (
          tr('允许后台活动'),
          tr('设置 → 应用和服务 → 应用启动管理，找到{0}，关闭「自动管理」，打开「允许自启动」「允许关联启动」「允许后台活动」。', [app]),
        ),
      PhoneSystem.vivo => (
          tr('允许后台高耗电'),
          tr('设置 → 电池 → 后台耗电管理，把{0}设为「允许后台高耗电」，并在应用设置中打开「自启动」。', [app]),
        ),
      PhoneSystem.samsung => (
          tr('电池设为不受限制'),
          tr('在应用设置中打开「电池」，选择「不受限制」。'),
        ),
      PhoneSystem.other => (
          tr('允许后台运行'),
          tr('在应用设置中找到「电池」或「耗电管理」，选择「不限制」或「允许后台运行」，并打开「自启动」（如有）。'),
        ),
    };
    final lockBody = switch (sys) {
      PhoneSystem.coloros => tr('打开最近任务，点{0}卡片右上角的菜单（或下拉卡片），选择「锁定」。', [app]),
      PhoneSystem.miui || PhoneSystem.vivo => tr('打开最近任务，长按{0}的卡片，点锁形图标。', [app]),
      PhoneSystem.huawei => tr('打开最近任务，下拉{0}的卡片，出现锁形图标即已锁定。', [app]),
      _ => tr('如果最近任务里有「锁定」选项，锁定{0}，清理后台时就不会关闭它。', [app]),
    };
    var n = 0;
    return ConstrainedBox(
      constraints: BoxConstraints(maxHeight: MediaQuery.sizeOf(context).height * 0.85),
      child: SafeArea(
        top: false,
        child: ListView(shrinkWrap: true, padding: const EdgeInsets.fromLTRB(22, 0, 22, 20), children: [
          Text(tr('保持后台运行'), style: AppText.title),
          const SizedBox(height: 6),
          Text(
            tr('很多手机会为了省电在后台关闭应用，VPN 也会随之断开。完成下面的设置后，{0}可以一直保持连接。', [app]),
            style: AppText.bodySmall.copyWith(color: AppColors.sub),
          ),
          const SizedBox(height: 14),
          _Step(
            n: ++n,
            title: tr('关闭电池优化'),
            body: tr('在系统弹窗中选择「允许」，让{0}不受电池优化限制。', [app]),
            action: s == null
                ? null
                : s.ignoringBattery
                    ? _Done(text: tr('已开启'))
                    : FilledButton.tonal(onPressed: BackgroundRun.requestIgnoreBattery, child: Text(tr('去开启'))),
          ),
          _Step(
            n: ++n,
            title: settingsTitle,
            body: settingsBody,
            action: OutlinedButton(onPressed: BackgroundRun.openAppSettings, child: Text(tr('打开应用设置'))),
          ),
          _Step(n: ++n, title: tr('在最近任务中锁定'), body: lockBody),
          if (sys == PhoneSystem.coloros)
            _Step(
              n: ++n,
              title: tr('关闭睡眠待机优化'),
              body: tr('设置 → 电池 → 更多设置，关闭「睡眠待机优化」，避免夜间被断网、关闭应用。'),
            ),
          const SizedBox(height: 4),
          Text(tr('不同手机和系统版本的菜单名称可能略有不同。'), style: AppText.caption.copyWith(color: AppColors.faint)),
        ]),
      ),
    );
  }
}

class _Step extends StatelessWidget {
  const _Step({required this.n, required this.title, required this.body, this.action});
  final int n;
  final String title, body;
  final Widget? action;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.only(bottom: 16),
      child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Container(
          width: 24,
          height: 24,
          alignment: Alignment.center,
          decoration: BoxDecoration(color: AppColors.sunken, shape: BoxShape.circle),
          child: Text('$n', style: const TextStyle(fontSize: 13, fontWeight: FontWeight.w700)),
        ),
        const SizedBox(width: 12),
        Expanded(
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Text(title, style: AppText.subtitle),
            const SizedBox(height: 3),
            Text(body, style: AppText.bodySmall.copyWith(color: AppColors.sub)),
            if (action != null) ...[const SizedBox(height: 8), action!],
          ]),
        ),
      ]),
    );
  }
}

class _Done extends StatelessWidget {
  const _Done({required this.text});
  final String text;
  @override
  Widget build(BuildContext context) {
    return Row(mainAxisSize: MainAxisSize.min, children: [
      const Icon(Icons.check_circle_rounded, size: 18, color: AppColors.signal),
      const SizedBox(width: 6),
      Text(text, style: const TextStyle(color: AppColors.signal, fontWeight: FontWeight.w600)),
    ]);
  }
}
