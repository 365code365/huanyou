import 'package:flutter/material.dart';

import '../api.dart';

class LoginPage extends StatefulWidget {
  const LoginPage({super.key, required this.api, required this.onLogin});
  final ApiClient api;
  final Future<void> Function(AuthResult) onLogin;

  @override
  State<LoginPage> createState() => _LoginPageState();
}

class _LoginPageState extends State<LoginPage> {
  final _form = GlobalKey<FormState>();
  final _email = TextEditingController();
  final _password = TextEditingController();
  bool _register = false;
  bool _busy = false;
  String? _error;

  @override
  void dispose() {
    _email.dispose();
    _password.dispose();
    super.dispose();
  }

  Future<void> _submit() async {
    if (!_form.currentState!.validate()) return;
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final email = _email.text.trim();
      final r = _register
          ? await widget.api.register(email, _password.text)
          : await widget.api.login(email, _password.text);
      await widget.onLogin(r);
    } on ApiException catch (e) {
      setState(() => _error = e.message);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Scaffold(
      body: SafeArea(
        child: Center(
          child: SingleChildScrollView(
            padding: const EdgeInsets.all(24),
            child: Form(
              key: _form,
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Icon(Icons.shield_rounded, size: 72, color: theme.colorScheme.primary),
                  const SizedBox(height: 12),
                  Text(_register ? '创建账号' : '登录',
                      textAlign: TextAlign.center, style: theme.textTheme.headlineSmall),
                  const SizedBox(height: 32),
                  TextFormField(
                    controller: _email,
                    keyboardType: TextInputType.emailAddress,
                    autofillHints: const [AutofillHints.email],
                    decoration: const InputDecoration(labelText: '邮箱', border: OutlineInputBorder()),
                    validator: (v) => (v == null || !v.contains('@')) ? '请输入有效邮箱' : null,
                  ),
                  const SizedBox(height: 16),
                  TextFormField(
                    controller: _password,
                    obscureText: true,
                    autofillHints: const [AutofillHints.password],
                    decoration: const InputDecoration(labelText: '密码', border: OutlineInputBorder()),
                    validator: (v) => (v == null || v.length < 8) ? '密码至少 8 位' : null,
                    onFieldSubmitted: (_) => _submit(),
                  ),
                  if (_error != null) ...[
                    const SizedBox(height: 16),
                    Text(_error!, style: TextStyle(color: theme.colorScheme.error)),
                  ],
                  const SizedBox(height: 24),
                  FilledButton(
                    onPressed: _busy ? null : _submit,
                    style: FilledButton.styleFrom(minimumSize: const Size.fromHeight(52)),
                    child: _busy
                        ? const SizedBox.square(dimension: 22, child: CircularProgressIndicator(strokeWidth: 2))
                        : Text(_register ? '注册并登录' : '登录'),
                  ),
                  TextButton(
                    onPressed: _busy ? null : () => setState(() => _register = !_register),
                    child: Text(_register ? '已有账号？去登录' : '没有账号？注册'),
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}
