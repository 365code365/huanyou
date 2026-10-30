import 'package:flutter/material.dart';

import '../../core/api.dart';
import '../../state/app_scope.dart';
import '../../theme/app_theme.dart';
import '../../widgets/app_text_field.dart';
import '../../widgets/primary_button.dart';
import '../../i18n/i18n.dart';

/// Login and registration on one screen, switched by a segmented toggle.
class AuthPage extends StatefulWidget {
  const AuthPage({super.key});

  @override
  State<AuthPage> createState() => _AuthPageState();
}

class _AuthPageState extends State<AuthPage> {
  final _form = GlobalKey<FormState>();
  final _email = TextEditingController();
  final _password = TextEditingController();
  final _confirm = TextEditingController();
  final _invite = TextEditingController();
  bool _register = false;
  bool _busy = false;
  String? _error;

  @override
  void dispose() {
    for (final c in [_email, _password, _confirm, _invite]) {
      c.dispose();
    }
    super.dispose();
  }

  void _switch(bool register) {
    if (register == _register) return;
    setState(() {
      _register = register;
      _error = null;
    });
    _form.currentState?.reset();
  }

  Future<void> _submit() async {
    FocusScope.of(context).unfocus();
    if (!_form.currentState!.validate()) return;
    setState(() {
      _busy = true;
      _error = null;
    });
    final app = AppScope.read(context);
    try {
      if (_register) {
        await app.register(_email.text.trim(), _password.text, inviteCode: _invite.text);
      } else {
        await app.login(_email.text.trim(), _password.text);
      }
    } on ApiException catch (e) {
      if (mounted) setState(() => _error = e.message);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final top = MediaQuery.paddingOf(context).top;
    return Scaffold(
      backgroundColor: AppColors.surface,
      body: SingleChildScrollView(
        keyboardDismissBehavior: ScrollViewKeyboardDismissBehavior.onDrag,
        child: Column(children: [
          _Header(topPadding: top),
          Transform.translate(
            offset: const Offset(0, -28),
            child: Container(
              decoration: BoxDecoration(
                color: AppColors.surface,
                borderRadius: BorderRadius.vertical(top: Radius.circular(28)),
              ),
              padding: const EdgeInsets.fromLTRB(24, 28, 24, 24),
              child: Form(
                key: _form,
                child: AutofillGroup(
                  child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
                    _ModeSwitch(register: _register, onChanged: _switch),
                    const SizedBox(height: 26),
                    Text(_register ? tr('创建账号') : tr('欢迎回来'),
                        style: const TextStyle(fontSize: 24, fontWeight: FontWeight.w700)),
                    const SizedBox(height: 6),
                    Text(_register ? tr('注册即送免费流量，填写邀请码还能多得时长') : tr('登录后一键连接'),
                        style: TextStyle(color: AppColors.sub, fontSize: 14)),
                    const SizedBox(height: 24),
                    AppTextField(
                      fieldKey: const Key('auth-email'),
                      controller: _email,
                      hint: tr('邮箱'),
                      icon: Icons.mail_outline_rounded,
                      keyboardType: TextInputType.emailAddress,
                      textInputAction: TextInputAction.next,
                      autofillHints: const [AutofillHints.email],
                      validator: (v) {
                        final s = v?.trim() ?? '';
                        return RegExp(r'^[^@\s]+@[^@\s]+\.[^@\s]+$').hasMatch(s) ? null : tr('请输入有效的邮箱');
                      },
                    ),
                    const SizedBox(height: 14),
                    AppTextField(
                      fieldKey: const Key('auth-password'),
                      controller: _password,
                      hint: _register ? tr('设置密码（至少 8 位）') : tr('密码'),
                      icon: Icons.lock_outline_rounded,
                      password: true,
                      textInputAction: _register ? TextInputAction.next : TextInputAction.done,
                      // No newPassword hint: without an associated domain, iOS "Strong Password"
                      // would silently fill a generated password the user never sees.
                      autofillHints: _register ? null : const [AutofillHints.password],
                      validator: (v) => (v == null || v.length < 8) ? tr('密码至少 8 位') : null,
                      onSubmitted: _register ? null : (_) => _submit(),
                    ),
                    AnimatedSize(
                      duration: const Duration(milliseconds: 220),
                      curve: Curves.easeOut,
                      child: _register
                          ? Column(children: [
                              const SizedBox(height: 14),
                              AppTextField(
                                fieldKey: const Key('auth-confirm'),
                                controller: _confirm,
                                hint: tr('确认密码'),
                                icon: Icons.lock_reset_rounded,
                                password: true,
                                textInputAction: TextInputAction.next,
                                validator: (v) => v != _password.text ? tr('两次输入的密码不一致') : null,
                              ),
                              const SizedBox(height: 14),
                              AppTextField(
                                fieldKey: const Key('auth-invite'),
                                controller: _invite,
                                hint: tr('邀请码（选填）'),
                                icon: Icons.card_giftcard_rounded,
                                textCapitalization: TextCapitalization.characters,
                                textInputAction: TextInputAction.done,
                                onSubmitted: (_) => _submit(),
                              ),
                            ])
                          : const SizedBox(width: double.infinity),
                    ),
                    AnimatedSwitcher(
                      duration: const Duration(milliseconds: 200),
                      child: _error == null
                          ? const SizedBox(height: 24)
                          : Padding(
                              key: ValueKey(_error),
                              padding: const EdgeInsets.symmetric(vertical: 14),
                              child: Container(
                                padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 11),
                                decoration: BoxDecoration(
                                  color: AppColors.danger.withValues(alpha: 0.08),
                                  borderRadius: BorderRadius.circular(12),
                                ),
                                child: Row(children: [
                                  const Icon(Icons.error_outline_rounded, color: AppColors.danger, size: 18),
                                  const SizedBox(width: 8),
                                  Expanded(
                                      child: Text(_error!,
                                          style: const TextStyle(color: AppColors.danger, fontSize: 13))),
                                ]),
                              ),
                            ),
                    ),
                    PrimaryButton(
                      key: const Key('auth-submit'),
                      label: _register ? tr('注册并登录') : tr('登录'),
                      loading: _busy,
                      onPressed: _submit,
                    ),
                    const SizedBox(height: 18),
                    Center(
                      child: TextButton(
                        onPressed: _busy ? null : () => _switch(!_register),
                        child: Text.rich(TextSpan(
                          style: TextStyle(color: AppColors.sub, fontSize: 14),
                          children: [
                            TextSpan(text: _register ? tr('已有账号？') : tr('还没有账号？')),
                            TextSpan(
                              text: _register ? tr('去登录') : tr('免费注册'),
                              style: TextStyle(color: AppColors.primary, fontWeight: FontWeight.w600),
                            ),
                          ],
                        )),
                      ),
                    ),
                  ]),
                ),
              ),
            ),
          ),
        ]),
      ),
    );
  }
}

class _Header extends StatelessWidget {
  const _Header({required this.topPadding});
  final double topPadding;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: double.infinity,
      clipBehavior: Clip.hardEdge,
      decoration: BoxDecoration(gradient: AppColors.brandGradient),
      child: Stack(children: [
        Positioned(right: -50, top: topPadding - 10, child: _Bubble(size: 190, alpha: 0.10)),
        Positioned(right: 90, bottom: 10, child: _Bubble(size: 90, alpha: 0.08)),
        Padding(
          padding: EdgeInsets.fromLTRB(24, topPadding + 36, 24, 64),
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Container(
            width: 56,
            height: 56,
            decoration: BoxDecoration(
              color: Colors.white.withValues(alpha: 0.18),
              borderRadius: BorderRadius.circular(18),
            ),
            child: const Icon(Icons.shield_rounded, color: Colors.white, size: 30),
          ),
          const SizedBox(height: 22),
          Text(tr('环游'),
              style: TextStyle(color: Colors.white, fontSize: 28, fontWeight: FontWeight.w700, letterSpacing: 1)),
          const SizedBox(height: 6),
          Text(tr('加密 · 稳定 · 一键直达全球'),
              style: TextStyle(color: Colors.white.withValues(alpha: 0.85), fontSize: 14)),
          ]),
        ),
      ]),
    );
  }
}

class _Bubble extends StatelessWidget {
  const _Bubble({required this.size, required this.alpha});
  final double size;
  final double alpha;

  @override
  Widget build(BuildContext context) => Container(
        width: size,
        height: size,
        decoration: BoxDecoration(shape: BoxShape.circle, color: Colors.white.withValues(alpha: alpha)),
      );
}

class _ModeSwitch extends StatelessWidget {
  const _ModeSwitch({required this.register, required this.onChanged});
  final bool register;
  final ValueChanged<bool> onChanged;

  @override
  Widget build(BuildContext context) {
    Widget tab(String text, bool value) => Expanded(
          child: GestureDetector(
            onTap: () => onChanged(value),
            behavior: HitTestBehavior.opaque,
            child: Center(
              child: AnimatedDefaultTextStyle(
                duration: const Duration(milliseconds: 200),
                style: TextStyle(
                  fontSize: 15,
                  fontWeight: register == value ? FontWeight.w600 : FontWeight.w400,
                  color: register == value ? AppColors.ink : AppColors.sub,
                ),
                child: Text(text),
              ),
            ),
          ),
        );
    return Container(
      height: 46,
      padding: const EdgeInsets.all(4),
      decoration: BoxDecoration(color: AppColors.canvas, borderRadius: BorderRadius.circular(14)),
      child: Stack(children: [
        AnimatedAlign(
          duration: const Duration(milliseconds: 220),
          curve: Curves.easeOutCubic,
          alignment: register ? Alignment.centerRight : Alignment.centerLeft,
          child: FractionallySizedBox(
            widthFactor: 0.5,
            child: Container(
              decoration: BoxDecoration(
                color: AppColors.surface,
                borderRadius: BorderRadius.circular(11),
                boxShadow: [BoxShadow(color: Colors.black.withValues(alpha: 0.06), blurRadius: 8, offset: const Offset(0, 2))],
              ),
            ),
          ),
        ),
        Row(children: [tab(tr('登录'), false), tab(tr('注册'), true)]),
      ]),
    );
  }
}
