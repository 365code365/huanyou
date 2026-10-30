import 'package:package_info_plus/package_info_plus.dart';

import 'api.dart';
import 'device.dart';
import 'open_url.dart';
import '../i18n/i18n.dart';

/// Checks for app updates published in the admin panel and opens them in the
/// system browser.
class Updater {
  static Future<PackageInfo> info() => PackageInfo.fromPlatform();

  /// The pubspec build number (the part after +). Per-ABI Android APKs carry
  /// versionCode = abi * 1000 + build (Flutter's --split-per-abi scheme), so
  /// strip the ABI prefix; build numbers must therefore stay below 1000.
  static int buildOf(PackageInfo i) => (int.tryParse(i.buildNumber) ?? 0) % 1000;

  static Future<AppUpdate?> check(ApiClient api) async {
    final platform = Device.platform;
    if (platform.isEmpty) return null;
    final build = buildOf(await info());
    return api.appUpdate(platform, build, abi: await Device.abi());
  }

  /// Opens the release in the system browser, which downloads the APK and
  /// hands it to the installer (or opens TestFlight / the App Store / the
  /// itms-services link on iOS). In-app downloading failed on many phones
  /// (vendor installers block packages from app-private storage), so the
  /// browser is the reliable path.
  static Future<void> install(AppUpdate u) async {
    if (u.url.isEmpty) throw UpdateException(tr('没有可用的下载地址'));
    if (!await openUrl(Uri.parse(u.url))) throw UpdateException(tr('无法打开浏览器，请稍后在官网下载'));
  }
}

class UpdateException implements Exception {
  UpdateException(this.message);
  final String message;
  @override
  String toString() => message;
}

