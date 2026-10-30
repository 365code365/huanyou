import 'dart:convert';
import 'dart:ui' as ui;

import 'package:flutter/widgets.dart';

import '../core/api.dart';
import '../core/prefs.dart';

/// Translates a UI string. The Chinese source text is the key; packs map it
/// to the target language and anything missing falls back to Chinese.
/// Placeholders are positional: tr('剩余 {0} 天', [days]).
String tr(String key, [List<Object?> args = const []]) {
  var s = I18n.instance._strings[key] ?? key;
  for (var i = 0; i < args.length; i++) {
    s = s.replaceAll('{$i}', '${args[i]}');
  }
  return s;
}

/// A language offered by the server (packs are managed in the admin panel).
class LangInfo {
  const LangInfo({required this.code, required this.name, required this.nativeName, this.rtl = false, this.version = 0});
  LangInfo.fromJson(Map<String, dynamic> j)
      : code = j['code'] as String,
        name = j['name'] as String? ?? '',
        nativeName = j['native_name'] as String? ?? j['code'] as String,
        rtl = j['rtl'] as bool? ?? false,
        version = (j['version'] as num?)?.toInt() ?? 0;
  final String code; // BCP-47-ish: en, es, pt-BR, ar ...
  final String name; // Chinese name for the admin, e.g. 英语
  final String nativeName; // shown in the picker, e.g. English
  final bool rtl;
  final int version;
  Map<String, dynamic> toJson() =>
      {'code': code, 'name': name, 'native_name': nativeName, 'rtl': rtl, 'version': version};
}

/// Language state: the user's preference ("system" or a code), the active
/// pack and the server catalog. Packs are downloaded on demand and cached, so
/// startup never waits on the network.
class I18n extends ChangeNotifier {
  I18n._();
  static final I18n instance = I18n._();

  static const base = 'zh';
  static const system = 'system';
  static const baseInfo = LangInfo(code: base, name: '简体中文', nativeName: '简体中文');
  static const _kPref = 'lang_pref';
  static const _kCatalog = 'lang_catalog';
  static const _kPack = 'lang_pack'; // {code, version, rtl, strings}

  String _pref = system;
  String _active = base;
  bool _rtl = false;
  int _version = 0;
  Map<String, String> _strings = const {};
  List<LangInfo> catalog = const [];
  String? downloading; // code being fetched, for the picker spinner

  String get preference => _pref;
  String get active => _active;

  Locale get locale {
    final parts = _active.split(RegExp('[-_]'));
    return Locale(parts.first, parts.length > 1 ? parts[1] : null);
  }

  TextDirection get direction => _rtl ? TextDirection.rtl : TextDirection.ltr;

  /// Restores the cached preference and pack from [Prefs] (already loaded).
  void load() {
    try {
      _pref = Prefs.get(_kPref) ?? system;
      final c = Prefs.get(_kCatalog);
      if (c != null) {
        catalog = [for (final e in jsonDecode(c) as List) LangInfo.fromJson(e as Map<String, dynamic>)];
      }
      final p = Prefs.get(_kPack);
      if (p != null) _apply(jsonDecode(p) as Map<String, dynamic>);
      // Cached pack no longer matches the preference (e.g. system language changed).
      final want = _resolve();
      if (want == base) _useBase();
    } catch (e) {
      debugPrint('i18n load: $e');
    }
  }

  /// Refreshes the catalog and makes sure the wanted pack is current.
  Future<void> sync(ApiClient api) async {
    try {
      final list = await api.languages();
      final encoded = jsonEncode([for (final l in list) l.toJson()]);
      final changed = encoded != jsonEncode([for (final l in catalog) l.toJson()]);
      catalog = list;
      if (changed) {
        await Prefs.set(_kCatalog, encoded);
        notifyListeners(); // the language picker lists the new catalog
      }
    } catch (e) {
      debugPrint('i18n catalog: $e');
    }
    await _ensure(api, _resolve());
  }

  /// Sets the preference ("system" or a code) and switches immediately if the
  /// pack is available. Throws when the download fails.
  Future<void> choose(ApiClient api, String pref) async {
    final want = _resolveFor(pref);
    if (want != base && want != _active) {
      downloading = want;
      notifyListeners();
      try {
        await _download(api, want);
      } finally {
        downloading = null;
      }
    } else if (want == base) {
      _useBase();
      await Prefs.remove(_kPack);
    }
    _pref = pref;
    await Prefs.set(_kPref, pref);
    notifyListeners();
  }

  /// Language actually used for a preference: an explicit choice wins; for
  /// "system" the first device language with a pack (exact, then by
  /// language), then English, then Chinese.
  String _resolve() => _resolveFor(_pref);

  String _resolveFor(String pref) {
    final codes = {for (final l in catalog) l.code.toLowerCase(): l.code};
    if (pref != system) return pref == base || codes.containsKey(pref.toLowerCase()) ? pref : base;
    for (final loc in ui.PlatformDispatcher.instance.locales) {
      final lang = loc.languageCode.toLowerCase();
      if (lang == 'zh') {
        final hant = loc.scriptCode == 'Hant' || const {'TW', 'HK', 'MO'}.contains(loc.countryCode);
        return hant ? codes['zh-hant'] ?? base : base;
      }
      final exact = codes['$lang-${(loc.countryCode ?? '').toLowerCase()}'];
      if (exact != null) return exact;
      final byLang = codes[lang] ?? codes.entries.where((e) => e.key.startsWith('$lang-')).firstOrNull?.value;
      if (byLang != null) return byLang;
    }
    return codes['en'] ?? base;
  }

  Future<void> _ensure(ApiClient api, String code) async {
    if (code == base) {
      if (_active != base) {
        _useBase();
        await Prefs.remove(_kPack);
        notifyListeners();
      }
      return;
    }
    final info = catalog.where((l) => l.code == code).firstOrNull;
    if (code == _active && (info == null || info.version <= _version)) return;
    try {
      await _download(api, code);
      notifyListeners();
    } catch (e) {
      debugPrint('i18n pack $code: $e');
    }
  }

  Future<void> _download(ApiClient api, String code) async {
    final pack = await api.languagePack(code);
    _apply(pack);
    await Prefs.set(_kPack, jsonEncode(pack));
  }

  void _apply(Map<String, dynamic> pack) {
    _active = pack['code'] as String;
    _version = (pack['version'] as num?)?.toInt() ?? 0;
    _rtl = pack['rtl'] as bool? ?? false;
    _strings = {
      for (final e in (pack['strings'] as Map<String, dynamic>? ?? const {}).entries)
        if (e.value is String && (e.value as String).isNotEmpty) e.key: e.value as String,
    };
  }

  void _useBase() {
    _active = base;
    _version = 0;
    _rtl = false;
    _strings = const {};
  }
}

/// Rebuilds every widget under [context]. Used after a language or theme
/// switch, because tr() and AppColors are read directly rather than through
/// inherited widgets.
void rebuildAll(BuildContext context) {
  void mark(Element e) {
    e.markNeedsBuild();
    e.visitChildren(mark);
  }

  (context as Element).visitChildren(mark);
}
