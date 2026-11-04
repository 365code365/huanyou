import 'package:flutter/material.dart';

import '../../core/config.dart';
import '../../core/open_url.dart';
import '../../core/prefs.dart';
import '../../i18n/i18n.dart';
import '../../state/app_scope.dart';
import '../../theme/app_theme.dart';
import '../../widgets/brand_mark.dart';
import '../../widgets/primary_button.dart';

/// Whether the user has accepted the VpnService prominent disclosure.
///
/// Google Play's VpnService policy requires a prominent disclosure, shown in
/// the normal use of the app (not buried in a menu or only in a privacy
/// policy), that explains why the app uses VpnService and what data it
/// accesses, and that requires an affirmative action to consent. On the Google
/// Play build this screen is shown once, before the app can connect.
class VpnConsent {
  static const _k = 'vpn_consent';
  static bool get given => Prefs.get(_k) == '1';
  static Future<void> accept() => Prefs.set(_k, '1');
}

/// The VpnService prominent disclosure + consent screen (Google Play build).
class VpnDisclosurePage extends StatelessWidget {
  const VpnDisclosurePage({super.key, required this.onAccept});

  /// Runs after the user taps "同意并继续".
  final VoidCallback onAccept;

  @override
  Widget build(BuildContext context) {
    final base = AppConfig.apiBaseUrls.isEmpty ? null : AppConfig.apiBaseUrls.first;
    final privacy = AppScope.of(context).iapFace; // keep links inside the policy page only
    return Scaffold(
      backgroundColor: AppColors.canvas,
      body: SafeArea(
        child: Column(
          children: [
            Expanded(
              child: ListView(
                padding: const EdgeInsets.fromLTRB(28, 40, 28, 16),
                children: [
                  const Center(child: BrandMark(size: 72)),
                  const SizedBox(height: 18),
                  Center(
                    child: Text(tr('连接前请了解'),
                        style: const TextStyle(fontSize: 22, fontWeight: FontWeight.w800)),
                  ),
                  const SizedBox(height: 24),
                  _Point(
                    icon: Icons.vpn_lock_rounded,
                    title: tr('使用 VPN 建立加密连接'),
                    text: tr('{0} 使用系统 VPN 服务（VpnService）在你的设备和我们的服务器之间建立一条加密通道，'
                        '让你选择的应用和网站流量通过该通道访问网络。', [AppConfig.appName]),
                  ),
                  _Point(
                    icon: Icons.privacy_tip_outlined,
                    title: tr('我们访问哪些数据'),
                    text: tr('为了建立和维持连接，VPN 会处理你设备发出的网络流量。我们不记录你访问的网站和内容；'
                        '仅记录连接来源 IP 地址和连接次数等统计信息，用于保障服务稳定和安全防护。'),
                  ),
                  _Point(
                    icon: Icons.verified_user_outlined,
                    title: tr('你的选择'),
                    text: tr('只有在你点击连接后，VPN 才会启动。你可以随时在应用内断开，或在系统设置中移除 VPN 配置。'),
                  ),
                  const SizedBox(height: 8),
                  if (!privacy && base != null)
                    Center(
                      child: TextButton(
                        onPressed: () => openUrl(base.replace(path: '/legal/privacy')),
                        child: Text(tr('查看隐私政策')),
                      ),
                    ),
                ],
              ),
            ),
            Padding(
              padding: const EdgeInsets.fromLTRB(28, 4, 28, 20),
              child: Column(
                mainAxisSize: MainAxisSize.min,
                children: [
                  PrimaryButton(
                    key: const Key('vpn-consent-accept'),
                    label: tr('同意并继续'),
                    onPressed: () async {
                      await VpnConsent.accept();
                      onAccept();
                    },
                  ),
                  const SizedBox(height: 8),
                  Text(tr('点击「同意并继续」即表示你同意上述 VPN 使用说明。'),
                      textAlign: TextAlign.center,
                      style: TextStyle(fontSize: 12, color: AppColors.faint)),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}

class _Point extends StatelessWidget {
  const _Point({required this.icon, required this.title, required this.text});
  final IconData icon;
  final String title, text;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.only(bottom: 20),
      child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Container(
          width: 38,
          height: 38,
          decoration: BoxDecoration(color: AppColors.primary.withValues(alpha: 0.1), borderRadius: BorderRadius.circular(11)),
          child: Icon(icon, size: 21, color: AppColors.primary),
        ),
        const SizedBox(width: 14),
        Expanded(
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Text(title, style: const TextStyle(fontSize: 15, fontWeight: FontWeight.w700)),
            const SizedBox(height: 4),
            Text(text, style: TextStyle(fontSize: 14, height: 1.6, color: AppColors.sub)),
          ]),
        ),
      ]),
    );
  }
}
