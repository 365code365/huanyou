import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:gal/gal.dart';
import 'package:qr_flutter/qr_flutter.dart';

import '../../core/api.dart';
import '../../core/config.dart';
import '../../state/app_scope.dart';
import '../../theme/app_theme.dart';
import '../../widgets/brand_mark.dart';
import '../../i18n/i18n.dart';

/// Invite page: share poster (QR + code) that can be saved to the album,
/// copyable download link, invite stats, and entering a friend's code.
class ReferralPage extends StatefulWidget {
  const ReferralPage({super.key});

  @override
  State<ReferralPage> createState() => _ReferralPageState();
}

class _ReferralPageState extends State<ReferralPage> {
  final _poster = GlobalKey();
  final _codeInput = TextEditingController();
  Referral? _ref;
  String? _error;
  bool _saving = false;
  bool _binding = false;

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void dispose() {
    _codeInput.dispose();
    super.dispose();
  }

  Future<void> _load() async {
    try {
      final r = await AppScope.read(context).api.referral();
      if (mounted) setState(() => _ref = r);
    } on ApiException catch (e) {
      if (mounted) setState(() => _error = e.message);
    }
  }

  void _snack(String msg) => ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(msg)));

  void _copy(String text, String what) {
    Clipboard.setData(ClipboardData(text: text));
    HapticFeedback.lightImpact();
    _snack(tr('{0}已复制', [what]));
  }

  /// Renders the poster widget to a PNG and saves it to the photo album.
  Future<void> _savePoster() async {
    setState(() => _saving = true);
    try {
      if (!await Gal.hasAccess(toAlbum: true) && !await Gal.requestAccess(toAlbum: true)) {
        _snack(tr('需要相册权限才能保存'));
        return;
      }
      final boundary = _poster.currentContext!.findRenderObject()! as RenderRepaintBoundary;
      final image = await boundary.toImage(pixelRatio: 3);
      final png = await image.toByteData(format: ui.ImageByteFormat.png);
      await Gal.putImageBytes(png!.buffer.asUint8List(), album: AppConfig.appName, name: 'invite_${_ref!.code}');
      HapticFeedback.mediumImpact();
      _snack(tr('二维码已保存到相册'));
    } on GalException catch (e) {
      _snack(tr('保存失败：{0}', [e.type.message]));
    } catch (_) {
      _snack(tr('保存失败，请重试'));
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  Future<void> _bind() async {
    final code = _codeInput.text.trim().toUpperCase();
    if (code.length != 7) return _snack(tr('邀请码是 7 位字母数字'));
    setState(() => _binding = true);
    try {
      final app = AppScope.read(context);
      final days = await app.api.bindReferral(code);
      await app.refreshUser();
      _codeInput.clear();
      FocusManager.instance.primaryFocus?.unfocus();
      _snack(days > 0 ? tr('已领取 {0} 天会员', [days]) : tr('邀请码已填写'));
      await _load();
    } on ApiException catch (e) {
      _snack(e.message);
    } finally {
      if (mounted) setState(() => _binding = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final r = _ref;
    return Scaffold(
      appBar: AppBar(title: Text(tr('邀请好友'))),
      body: _error != null
          ? Center(child: Text(_error!, style: TextStyle(color: AppColors.sub)))
          : r == null
              ? const Center(child: CircularProgressIndicator())
              : !r.enabled
                  ? Center(child: Text(tr('邀请活动暂未开启'), style: TextStyle(color: AppColors.sub)))
                  : ListView(padding: const EdgeInsets.fromLTRB(18, 4, 18, 40), children: [
                      _RewardBanner(ref: r),
                      const SizedBox(height: 16),
                      Center(
                        child: RepaintBoundary(key: _poster, child: _Poster(ref: r)),
                      ),
                      const SizedBox(height: 16),
                      Row(children: [
                        Expanded(
                          child: _ActionButton(
                            key: const Key('save-qr'),
                            icon: Icons.download_rounded,
                            label: _saving ? tr('保存中…') : tr('保存二维码'),
                            primary: true,
                            onTap: _saving || r.inviteUrl.isEmpty ? null : _savePoster,
                          ),
                        ),
                        const SizedBox(width: 10),
                        Expanded(
                          child: _ActionButton(
                            key: const Key('copy-link'),
                            icon: Icons.link_rounded,
                            label: tr('复制下载链接'),
                            onTap: r.shareText.isEmpty ? null : () => _copy(r.shareText, tr('下载链接')),
                          ),
                        ),
                      ]),
                      const SizedBox(height: 10),
                      Card(
                        clipBehavior: Clip.antiAlias,
                        child: Column(children: [
                          ListTile(
                            title: Text(tr('我的邀请码')),
                            subtitle: Text(r.code,
                                key: const Key('ref-code'),
                                style: TextStyle(
                                    fontSize: 18, fontWeight: FontWeight.w800, letterSpacing: 3, color: AppColors.ink)),
                            trailing: TextButton.icon(
                              onPressed: () => _copy(r.code, tr('邀请码')),
                              icon: const Icon(Icons.copy_rounded, size: 17),
                              label: Text(tr('复制')),
                            ),
                          ),
                          const Divider(indent: 16),
                          ListTile(
                            title: Text(tr('已成功邀请')),
                            trailing: Text(tr('{0} 人', [r.invitedCount]),
                                style: const TextStyle(fontSize: 16, fontWeight: FontWeight.w700)),
                          ),
                        ]),
                      ),
                      if (r.canBind) ...[
                        const SizedBox(height: 14),
                        Card(
                          child: Padding(
                            padding: const EdgeInsets.fromLTRB(16, 14, 12, 14),
                            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                              Text(r.inviteeDays > 0 ? tr('有好友的邀请码？填写后领取 {0} 天会员', [r.inviteeDays]) : tr('填写好友的邀请码'),
                                  style: const TextStyle(fontSize: 14, fontWeight: FontWeight.w600)),
                              const SizedBox(height: 10),
                              Row(children: [
                                Expanded(
                                  child: TextField(
                                    controller: _codeInput,
                                    textCapitalization: TextCapitalization.characters,
                                    maxLength: 7,
                                    inputFormatters: [FilteringTextInputFormatter.allow(RegExp('[a-zA-Z0-9]'))],
                                    decoration: InputDecoration(hintText: tr('7 位邀请码'), counterText: '', isDense: true),
                                    style: const TextStyle(letterSpacing: 3, fontWeight: FontWeight.w700),
                                  ),
                                ),
                                const SizedBox(width: 10),
                                SizedBox(
                                  height: 46,
                                  child: FilledButton(
                                    onPressed: _binding ? null : _bind,
                                    child: _binding
                                        ? const SizedBox.square(
                                            dimension: 18, child: CircularProgressIndicator(strokeWidth: 2, color: Colors.white))
                                        : Text(tr('领取')),
                                  ),
                                ),
                              ]),
                            ]),
                          ),
                        ),
                      ],
                      Padding(
                        padding: EdgeInsets.fromLTRB(6, 16, 6, 0),
                        child: Text(
                          tr('好友扫码或打开链接下载，首次打开会自动识别你的邀请码，奖励即时到账。'),
                          style: TextStyle(color: AppColors.sub, fontSize: 12.5, height: 1.6),
                        ),
                      ),
                    ]),
    );
  }
}

class _RewardBanner extends StatelessWidget {
  const _RewardBanner({required this.ref});
  final Referral ref;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.fromLTRB(20, 18, 20, 18),
      decoration: BoxDecoration(color: AppColors.hero, borderRadius: BorderRadius.circular(AppRadius.card)),
      child: Row(children: [
        Expanded(
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Text(tr('邀请好友，双方得会员'), style: TextStyle(color: Colors.white, fontSize: 17, fontWeight: FontWeight.w700)),
            const SizedBox(height: 4),
            Text(ref.maxInvites > 0 ? tr('最多奖励 {0} 位好友', [ref.maxInvites]) : tr('邀请越多，时长越多'),
                style: const TextStyle(color: Color(0xFF9A9CA3), fontSize: 12.5)),
          ]),
        ),
        _Reward(days: ref.inviterDays, label: tr('你得')),
        Container(width: 1, height: 32, margin: const EdgeInsets.symmetric(horizontal: 14), color: Colors.white12),
        _Reward(days: ref.inviteeDays, label: tr('好友得')),
      ]),
    );
  }
}

class _Reward extends StatelessWidget {
  const _Reward({required this.days, required this.label});
  final int days;
  final String label;

  @override
  Widget build(BuildContext context) => Column(children: [
        Text.rich(TextSpan(children: [
          TextSpan(text: '$days', style: const TextStyle(fontSize: 24, fontWeight: FontWeight.w800)),
          TextSpan(text: tr(' 天'), style: TextStyle(fontSize: 12)),
        ]), style: const TextStyle(color: Colors.white)),
        Text(label, style: const TextStyle(color: Color(0xFF9A9CA3), fontSize: 12)),
      ]);
}

/// The shareable poster. Saved as an image, so it carries its own background
/// and everything needed to install (name, QR, code) without the app around it.
class _Poster extends StatelessWidget {
  const _Poster({required this.ref});
  final Referral ref;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: 300,
      padding: const EdgeInsets.fromLTRB(24, 24, 24, 20),
      decoration: BoxDecoration(
        color: AppColors.night,
        borderRadius: BorderRadius.circular(24),
        gradient: const RadialGradient(
          center: Alignment(0.9, -1.1),
          radius: 1.3,
          colors: [Color(0xFF1C4A36), AppColors.night],
        ),
      ),
      child: Column(children: [
        Row(children: [
          const BrandMark(size: 34, inverted: true),
          const SizedBox(width: 10),
          Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            const Text(AppConfig.appName,
                style: TextStyle(color: Colors.white, fontSize: 18, fontWeight: FontWeight.w800, letterSpacing: 2)),
            Text(ref.slogan.isEmpty ? tr(AppConfig.slogan) : tr(ref.slogan),
                style: const TextStyle(color: Color(0xFF9C9B97), fontSize: 11.5)),
          ]),
        ]),
        const SizedBox(height: 20),
        Container(
          padding: const EdgeInsets.all(12),
          decoration: BoxDecoration(color: Colors.white, borderRadius: BorderRadius.circular(16)),
          child: ref.inviteUrl.isEmpty
              ? SizedBox.square(dimension: 180, child: Center(child: Text(tr('暂无下载链接'))))
              : QrImageView(
                  data: ref.inviteUrl,
                  size: 180,
                  padding: EdgeInsets.zero,
                  errorCorrectionLevel: QrErrorCorrectLevel.M,
                  // Standard square modules: round dots look nicer but some
                  // scanners (and OpenCV) fail to read them.
                  eyeStyle: const QrEyeStyle(eyeShape: QrEyeShape.square, color: AppColors.night),
                  dataModuleStyle: const QrDataModuleStyle(dataModuleShape: QrDataModuleShape.square, color: AppColors.night),
                ),
        ),
        const SizedBox(height: 16),
        Text(tr('邀请码'), style: TextStyle(color: Color(0xFF9C9B97), fontSize: 11.5)),
        const SizedBox(height: 2),
        Text(ref.code,
            style: const TextStyle(color: Colors.white, fontSize: 24, fontWeight: FontWeight.w800, letterSpacing: 5)),
        const SizedBox(height: 8),
        Text(
          ref.inviteeDays > 0 ? tr('扫码下载 · 自动领取 {0} 天会员', [ref.inviteeDays]) : tr('扫码下载 · 无需注册'),
          style: const TextStyle(color: Color(0xFF7FD8A4), fontSize: 12.5, fontWeight: FontWeight.w600),
        ),
      ]),
    );
  }
}

class _ActionButton extends StatelessWidget {
  const _ActionButton({super.key, required this.icon, required this.label, required this.onTap, this.primary = false});
  final IconData icon;
  final String label;
  final VoidCallback? onTap;
  final bool primary;

  @override
  Widget build(BuildContext context) {
    final fg = primary ? AppColors.onInk : AppColors.ink;
    return SizedBox(
      height: 50,
      child: Material(
        color: primary ? AppColors.ink : AppColors.surface,
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(AppRadius.button),
          side: primary ? BorderSide.none : BorderSide(color: AppColors.line),
        ),
        child: InkWell(
          borderRadius: BorderRadius.circular(AppRadius.button),
          onTap: onTap,
          child: Opacity(
            opacity: onTap == null ? 0.5 : 1,
            child: Row(mainAxisAlignment: MainAxisAlignment.center, children: [
              Icon(icon, size: 19, color: fg),
              const SizedBox(width: 6),
              Text(label, style: TextStyle(color: fg, fontSize: 14.5, fontWeight: FontWeight.w600)),
            ]),
          ),
        ),
      ),
    );
  }
}
