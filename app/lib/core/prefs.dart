import 'package:flutter/foundation.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';

/// In-memory mirror of the app's secure storage.
///
/// Every secure-storage read is a platform-channel call that decrypts with the
/// Android Keystore / iOS Keychain (tens of ms each on low-end phones). The
/// app reads everything once at startup with a single readAll(), then serves
/// reads from memory; writes update memory immediately and persist in order.
class Prefs {
  Prefs._();

  // macOS: the login keychain. The data-protection keychain (the default)
  // needs a keychain-access-groups entitlement, i.e. a provisioned team build.
  static const _storage = FlutterSecureStorage(mOptions: MacOsOptions(usesDataProtectionKeychain: false));
  static Map<String, String> _m = {};
  static Future<void> _pending = Future.value();

  /// Loads all values. Safe to call more than once (later calls reload).
  static Future<void> load() async {
    try {
      _m = await _storage.readAll();
    } catch (e) {
      // A broken keystore entry (e.g. after a device restore) must not block
      // startup; the app then behaves like a fresh install.
      debugPrint('prefs load: $e');
      _m = {};
    }
  }

  static String? get(String key) => _m[key];

  /// Sets (or with null, deletes) a value. Returns when it is persisted.
  static Future<void> set(String key, String? value) {
    if (value == null) {
      _m.remove(key);
    } else {
      _m[key] = value;
    }
    // Chain writes so two quick updates of one key can't land out of order.
    return _pending = _pending.then((_) async {
      try {
        value == null ? await _storage.delete(key: key) : await _storage.write(key: key, value: value);
      } catch (e) {
        debugPrint('prefs write $key: $e');
      }
    });
  }

  static Future<void> remove(String key) => set(key, null);
}
