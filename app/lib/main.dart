import 'package:flutter/material.dart';

import 'api.dart';
import 'pages/home_page.dart';
import 'pages/login_page.dart';
import 'session.dart';

void main() {
  WidgetsFlutterBinding.ensureInitialized();
  runApp(const VpnApp());
}

class VpnApp extends StatefulWidget {
  const VpnApp({super.key});

  @override
  State<VpnApp> createState() => _VpnAppState();
}

class _VpnAppState extends State<VpnApp> {
  final api = ApiClient();
  final session = Session();
  bool? loggedIn;

  @override
  void initState() {
    super.initState();
    session.token().then((t) {
      api.token = t;
      setState(() => loggedIn = t != null);
    });
  }

  Future<void> _onLogin(AuthResult r) async {
    await session.saveToken(r.token);
    api.token = r.token;
    setState(() => loggedIn = true);
  }

  Future<void> _onLogout() async {
    await session.clear();
    api.token = null;
    setState(() => loggedIn = false);
  }

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: '安全连接',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(colorSchemeSeed: const Color(0xFF2F6BFF), useMaterial3: true),
      home: switch (loggedIn) {
        null => const Scaffold(body: Center(child: CircularProgressIndicator())),
        false => LoginPage(api: api, onLogin: _onLogin),
        true => HomePage(api: api, session: session, onLogout: _onLogout),
      },
    );
  }
}
