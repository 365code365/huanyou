import 'package:flutter/material.dart';

import '../../i18n/i18n.dart';
import '../../state/app_scope.dart';
import '../../theme/app_theme.dart';
import '../../theme/appearance.dart';

/// Label for the current language preference, e.g. "跟随系统 · English".
String languageLabel() {
  final i = I18n.instance;
  final name = i.active == I18n.base
      ? I18n.baseInfo.nativeName
      : i.catalog.where((l) => l.code == i.active).firstOrNull?.nativeName ?? i.active;
  return i.preference == I18n.system ? '${tr('跟随系统')} · $name' : name;
}

Future<void> showLanguageSheet(BuildContext context) async {
  final api = AppScope.read(context).api;
  // Refresh the list in the background; the sheet updates when it lands.
  I18n.instance.sync(api);
  await showModalBottomSheet<void>(
    context: context,
    showDragHandle: true,
    isScrollControlled: true,
    builder: (ctx) => ListenableBuilder(
      listenable: I18n.instance,
      builder: (ctx, _) {
        final i = I18n.instance;
        final options = [
          (I18n.system, tr('跟随系统'), ''),
          (I18n.base, I18n.baseInfo.nativeName, I18n.baseInfo.name),
          for (final l in i.catalog) (l.code, l.nativeName, ''),
        ];
        return ConstrainedBox(
          constraints: BoxConstraints(maxHeight: MediaQuery.sizeOf(ctx).height * 0.75),
          child: SafeArea(
            top: false,
            child: ListView(shrinkWrap: true, padding: const EdgeInsets.only(bottom: 12), children: [
              Padding(
                padding: const EdgeInsets.fromLTRB(22, 0, 22, 8),
                child: Text(tr('语言'), style: const TextStyle(fontSize: 17, fontWeight: FontWeight.w700)),
              ),
              for (final (code, title, sub) in options)
                ListTile(
                  key: Key('lang-$code'),
                  contentPadding: const EdgeInsets.symmetric(horizontal: 22),
                  title: Text(title, style: const TextStyle(fontWeight: FontWeight.w600)),
                  subtitle: sub.isEmpty || sub == title ? null : Text(sub, style: TextStyle(color: AppColors.sub)),
                  trailing: i.downloading == code
                      ? const SizedBox.square(dimension: 20, child: CircularProgressIndicator(strokeWidth: 2))
                      : i.preference == code
                          ? Icon(Icons.check_rounded, color: AppColors.signal)
                          : null,
                  onTap: i.downloading != null
                      ? null
                      : () async {
                          try {
                            await i.choose(api, code);
                            if (ctx.mounted) Navigator.pop(ctx);
                          } catch (e) {
                            if (ctx.mounted) {
                              ScaffoldMessenger.of(ctx).showSnackBar(SnackBar(content: Text(tr('语言包下载失败：{0}', [e]))));
                            }
                          }
                        },
                ),
            ]),
          ),
        );
      },
    ),
  );
}

Future<void> showAppearanceSheet(BuildContext context) async {
  await showModalBottomSheet<void>(
    context: context,
    showDragHandle: true,
    builder: (ctx) => ListenableBuilder(
      listenable: Appearance.instance,
      builder: (ctx, _) => SafeArea(
        top: false,
        child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.start, children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(22, 0, 22, 8),
            child: Text(tr('外观'), style: const TextStyle(fontSize: 17, fontWeight: FontWeight.w700)),
          ),
          for (final m in AppearanceMode.values)
            ListTile(
              key: Key('appearance-${m.id}'),
              contentPadding: const EdgeInsets.symmetric(horizontal: 22),
              leading: Icon(switch (m) {
                AppearanceMode.system => Icons.brightness_auto_rounded,
                AppearanceMode.light => Icons.light_mode_outlined,
                AppearanceMode.dark => Icons.dark_mode_outlined,
              }),
              title: Text(tr(m.label), style: const TextStyle(fontWeight: FontWeight.w600)),
              trailing: Appearance.instance.mode == m ? Icon(Icons.check_rounded, color: AppColors.signal) : null,
              onTap: () {
                Appearance.instance.set(m);
                Navigator.pop(ctx);
              },
            ),
          const SizedBox(height: 8),
        ]),
      ),
    ),
  );
}
