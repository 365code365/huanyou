import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:mobile_scanner/mobile_scanner.dart';
import 'package:qr_flutter/qr_flutter.dart';

import '../../core/api.dart';
import '../../state/app_scope.dart';
import '../../theme/app_theme.dart';
import '../plans/plans_page.dart';
import '../../i18n/i18n.dart';

/// Extracts a link code from scanned / pasted text: a bare code ("ABCD-EFGH")
/// or a link ".../l/ABCDEFGH". Returns the 8 code characters or null.
String? parseLinkCode(String raw) {
  var s = raw.trim();
  final i = s.lastIndexOf('/l/');
  if (i >= 0) s = s.substring(i + 3);
  s = s.toUpperCase().replaceAll(RegExp('[^A-Z0-9]'), '');
  return s.length == 8 ? s : null;
}

/// 多设备: share this subscription with up to N devices (owner), or follow
/// another account's subscription by scanning / entering its link code.
class DevicesPage extends StatefulWidget {
  const DevicesPage({super.key});

  @override
  State<DevicesPage> createState() => _DevicesPageState();
}

class _DevicesPageState extends State<DevicesPage> {
  DeviceShare? _share;
  String? _error;
  LinkCode? _code;
  bool _busy = false;
  Timer? _tick;

  @override
  void initState() {
    super.initState();
    _load();
    _tick = Timer.periodic(const Duration(seconds: 1), (_) {
      if (mounted && _code != null) setState(() {});
    });
  }

  @override
  void dispose() {
    _tick?.cancel();
    super.dispose();
  }

  Future<void> _load() async {
    try {
      final s = await AppScope.read(context).api.devices();
      if (!mounted) return;
      setState(() {
        _share = s;
        _error = null;
        if (s.code != null && s.codeExpiresAt != null) {
          _code = LinkCode.fromJson({
            'code': s.code, 'code_display': s.codeDisplay,
            'expires_at': s.codeExpiresAt!.toUtc().toIso8601String(), 'link': s.link,
          });
        }
      });
    } on ApiException catch (e) {
      if (mounted) setState(() => _error = e.message);
    }
  }

  void _snack(String m) => ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(m)));

  Future<void> _run(Future<void> Function() f) async {
    setState(() => _busy = true);
    try {
      await f();
    } on ApiException catch (e) {
      _snack(e.message);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _newCode() => _run(() async {
        final c = await AppScope.read(context).api.createLinkCode();
        setState(() => _code = c);
      });

  Future<void> _join(String code) => _run(() async {
        final app = AppScope.read(context);
        final u = await app.api.joinDevice(code);
        await app.refreshUser();
        HapticFeedback.mediumImpact();
        _snack(tr('已绑定 {0} 的订阅', [u.ownerCode]));
        await _load();
      });

  Future<void> _leave() async {
    final ok = await _confirm(tr('解除绑定'), tr('本机将不再使用 {0} 的订阅，恢复为自己的套餐。', [_share?.ownerCode]));
    if (ok) {
      await _run(() async {
        final app = AppScope.read(context);
        await app.api.leaveDevice();
        await app.refreshUser();
        _snack(tr('已解除绑定'));
        await _load();
      });
    }
  }

  Future<void> _remove(SharedDevice d) async {
    final ok = await _confirm(tr('移除设备'), tr('{0} 将无法再使用你的订阅。', [d.rechargeCode]));
    if (ok) {
      await _run(() async {
        await AppScope.read(context).api.removeDevice(d.id);
        _snack(tr('已移除 {0}', [d.rechargeCode]));
        await _load();
      });
    }
  }

  Future<bool> _confirm(String title, String body) async =>
      await showDialog<bool>(
        context: context,
        builder: (ctx) => AlertDialog(
          title: Text(title),
          content: Text(body),
          actions: [
            TextButton(onPressed: () => Navigator.pop(ctx, false), child: Text(tr('取消'))),
            FilledButton(onPressed: () => Navigator.pop(ctx, true), child: Text(tr('确定'))),
          ],
        ),
      ) ??
      false;

  Future<void> _scan() async {
    final raw = await Navigator.push<String>(context, MaterialPageRoute(builder: (_) => const _ScanPage()));
    if (raw == null || !mounted) return;
    final code = parseLinkCode(raw);
    if (code == null) return _snack(tr('不是有效的设备令牌二维码'));
    await _join(code);
  }

  Future<void> _enter() async {
    final ctl = TextEditingController();
    final clip = (await Clipboard.getData(Clipboard.kTextPlain))?.text;
    final fromClip = clip == null ? null : parseLinkCode(clip);
    if (fromClip != null) ctl.text = '${fromClip.substring(0, 4)}-${fromClip.substring(4)}';
    if (!mounted) return;
    final code = await showDialog<String>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: Text(tr('输入设备令牌')),
        content: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.start, children: [
          Text(tr('在已开通会员的设备上：我的 → 多设备，复制令牌或链接后粘贴到这里。'),
              style: TextStyle(fontSize: 13, color: AppColors.sub, height: 1.5)),
          const SizedBox(height: 14),
          TextField(
            controller: ctl,
            autofocus: fromClip == null,
            textCapitalization: TextCapitalization.characters,
            decoration: InputDecoration(hintText: tr('ABCD-EFGH 或链接')),
            style: const TextStyle(letterSpacing: 2, fontWeight: FontWeight.w700),
          ),
        ]),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx), child: Text(tr('取消'))),
          FilledButton(onPressed: () => Navigator.pop(ctx, ctl.text), child: Text(tr('绑定'))),
        ],
      ),
    );
    if (code == null) return;
    final c = parseLinkCode(code);
    if (c == null) return _snack(tr('令牌格式不正确，应为 8 位字母数字'));
    await _join(c);
  }

  @override
  Widget build(BuildContext context) {
    final s = _share;
    return Scaffold(
      appBar: AppBar(title: Text(tr('多设备'))),
      body: _error != null && s == null
          ? Center(child: Text(_error!, style: TextStyle(color: AppColors.sub)))
          : s == null
              ? const Center(child: CircularProgressIndicator())
              : RefreshIndicator(
                  color: AppColors.ink,
                  onRefresh: _load,
                  child: ListView(padding: const EdgeInsets.fromLTRB(18, 4, 18, 40), children: [
                    if (s.isMember) _memberCard(s) else _ownerCard(s),
                    const SizedBox(height: 16),
                    _deviceList(s),
                    if (!s.isMember) ...[
                      const SizedBox(height: 16),
                      _joinCard(),
                    ],
                    Padding(
                      padding: EdgeInsets.fromLTRB(6, 16, 6, 0),
                      child: Text(
                        tr('同一订阅的设备共用套餐时长和流量。令牌 10 分钟内有效，只能由会员本人生成；不用的设备可以随时移除。'),
                        style: TextStyle(color: AppColors.sub, fontSize: 12.5, height: 1.6),
                      ),
                    ),
                  ]),
                ),
    );
  }

  Widget _ownerCard(DeviceShare s) {
    if (!s.eligible) {
      return _Panel(children: [
        Icon(Icons.devices_rounded, size: 34, color: AppColors.ink),
        const SizedBox(height: 10),
        Text(tr('一个订阅最多 {0} 台设备', [s.maxDevices]), style: const TextStyle(fontSize: 17, fontWeight: FontWeight.w700)),
        const SizedBox(height: 6),
        Text(s.reason, textAlign: TextAlign.center, style: TextStyle(color: AppColors.sub, fontSize: 13, height: 1.5)),
        const SizedBox(height: 16),
        FilledButton(
          onPressed: () async {
            await openPlans(context);
            await _load();
          },
          child: Text(tr('升级会员')),
        ),
      ]);
    }
    final c = _code;
    final left = c == null ? Duration.zero : c.expiresAt.difference(DateTime.now());
    final valid = c != null && !left.isNegative;
    return _Panel(children: [
      Text(tr('让其它设备扫码，或输入下面的令牌'), style: TextStyle(fontSize: 14, color: AppColors.sub)),
      const SizedBox(height: 14),
      Container(
        padding: const EdgeInsets.all(12),
        decoration: BoxDecoration(color: Colors.white, borderRadius: BorderRadius.circular(16), border: Border.all(color: AppColors.line)),
        child: valid
            ? QrImageView(data: c.link.isNotEmpty ? c.link : c.code, size: 184, padding: EdgeInsets.zero)
            : SizedBox.square(
                dimension: 184,
                child: Center(
                  child: FilledButton.icon(
                    key: const Key('gen-code'),
                    onPressed: _busy ? null : _newCode,
                    icon: const Icon(Icons.qr_code_rounded, size: 18),
                    label: Text(c == null ? tr('生成令牌') : tr('令牌已过期，重新生成')),
                  ),
                ),
              ),
      ),
      if (valid) ...[
        const SizedBox(height: 14),
        Text(c.display,
            key: const Key('link-code'),
            style: const TextStyle(fontSize: 26, fontWeight: FontWeight.w800, letterSpacing: 4,
                fontFeatures: [FontFeature.tabularFigures()])),
        const SizedBox(height: 4),
        Text(tr('{0}:{1} 后失效', [left.inMinutes, (left.inSeconds % 60).toString().padLeft(2, '0')]),
            style: TextStyle(fontSize: 12.5, color: AppColors.sub, fontFeatures: [FontFeature.tabularFigures()])),
        const SizedBox(height: 12),
        Row(mainAxisAlignment: MainAxisAlignment.center, children: [
          TextButton.icon(
            onPressed: () {
              Clipboard.setData(ClipboardData(text: c.link.isNotEmpty ? tr('环游设备令牌 {0}\n{1}', [c.display, c.link]) : c.display));
              _snack(tr('令牌已复制'));
            },
            icon: const Icon(Icons.copy_rounded, size: 17),
            label: Text(tr('复制令牌')),
          ),
          TextButton.icon(
            onPressed: _busy ? null : _newCode,
            icon: const Icon(Icons.refresh_rounded, size: 18),
            label: Text(tr('换一个')),
          ),
        ]),
      ],
    ]);
  }

  Widget _memberCard(DeviceShare s) => _Panel(children: [
        const Icon(Icons.link_rounded, size: 34, color: AppColors.signal),
        const SizedBox(height: 10),
        Text(tr('本机正在使用 {0} 的订阅', [s.ownerCode]), style: const TextStyle(fontSize: 16, fontWeight: FontWeight.w700)),
        const SizedBox(height: 6),
        Text(tr('续费请在该订阅的主设备上操作'), style: TextStyle(color: AppColors.sub, fontSize: 13)),
        const SizedBox(height: 14),
        OutlinedButton(onPressed: _busy ? null : _leave, child: Text(tr('解除绑定'))),
      ]);

  Widget _deviceList(DeviceShare s) {
    return Card(
      clipBehavior: Clip.antiAlias,
      child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 14, 16, 6),
          child: Text(tr('已绑定设备 {0}/{1}', [s.devices.length, s.maxDevices]),
              style: const TextStyle(fontSize: 14, fontWeight: FontWeight.w700)),
        ),
        for (final d in s.devices)
          ListTile(
            leading: Icon(d.platform == 'ios' ? Icons.phone_iphone_rounded : Icons.phone_android_rounded, color: AppColors.ink),
            title: Text(d.rechargeCode, style: const TextStyle(fontWeight: FontWeight.w600)),
            subtitle: Text([if (d.owner) tr('主设备'), if (d.self) tr('本机')].join(' · ').ifEmpty(tr('共享设备'))),
            trailing: !s.isMember && !d.owner
                ? TextButton(onPressed: _busy ? null : () => _remove(d), child: Text(tr('移除'), style: TextStyle(color: AppColors.danger)))
                : null,
          ),
      ]),
    );
  }

  Widget _joinCard() => Card(
        child: Padding(
          padding: const EdgeInsets.all(16),
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Text(tr('本机使用别人的订阅'), style: TextStyle(fontSize: 14, fontWeight: FontWeight.w700)),
            const SizedBox(height: 4),
            Text(tr('扫描会员设备上的二维码，或输入它的令牌'), style: TextStyle(fontSize: 12.5, color: AppColors.sub)),
            const SizedBox(height: 12),
            Row(children: [
              Expanded(
                child: FilledButton.icon(
                  key: const Key('scan-join'),
                  onPressed: _busy ? null : _scan,
                  icon: const Icon(Icons.qr_code_scanner_rounded, size: 18),
                  label: Text(tr('扫码绑定')),
                ),
              ),
              const SizedBox(width: 10),
              Expanded(
                child: OutlinedButton.icon(
                  key: const Key('enter-join'),
                  onPressed: _busy ? null : _enter,
                  icon: const Icon(Icons.keyboard_rounded, size: 18),
                  label: Text(tr('输入令牌')),
                ),
              ),
            ]),
          ]),
        ),
      );
}

extension on String {
  String ifEmpty(String other) => isEmpty ? other : this;
}

class _Panel extends StatelessWidget {
  const _Panel({required this.children});
  final List<Widget> children;

  @override
  Widget build(BuildContext context) => Card(
        child: Padding(
          padding: const EdgeInsets.fromLTRB(20, 22, 20, 18),
          child: Center(child: Column(children: children)),
        ),
      );
}

/// Full-screen QR scanner; pops with the first barcode's raw text.
class _ScanPage extends StatefulWidget {
  const _ScanPage();

  @override
  State<_ScanPage> createState() => _ScanPageState();
}

class _ScanPageState extends State<_ScanPage> {
  bool _done = false;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.black,
      appBar: AppBar(
        backgroundColor: Colors.black,
        foregroundColor: Colors.white,
        title: Text(tr('扫描设备令牌'), style: TextStyle(color: Colors.white)),
      ),
      body: Stack(alignment: Alignment.center, children: [
        MobileScanner(
          onDetect: (capture) {
            final v = capture.barcodes.firstOrNull?.rawValue;
            if (_done || v == null) return;
            _done = true;
            Navigator.pop(context, v);
          },
          errorBuilder: (context, error) => Center(
            child: Text(tr('无法打开相机：{0}\n请在系统设置里允许相机权限', [error.errorCode.name]),
                textAlign: TextAlign.center, style: const TextStyle(color: Colors.white70)),
          ),
        ),
        IgnorePointer(
          child: Container(
            width: 240,
            height: 240,
            decoration: BoxDecoration(border: Border.all(color: Colors.white, width: 2), borderRadius: BorderRadius.circular(20)),
          ),
        ),
        Positioned(
          bottom: 60,
          child: Text(tr('对准会员设备「多设备」页的二维码'), style: TextStyle(color: Colors.white70)),
        ),
      ]),
    );
  }
}
