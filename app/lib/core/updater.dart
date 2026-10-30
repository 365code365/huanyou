import 'dart:io';

import 'package:crypto/crypto.dart';
import 'package:flutter/services.dart';
import 'package:http/http.dart' as http;
import 'package:package_info_plus/package_info_plus.dart';
import 'package:path_provider/path_provider.dart';

import 'api.dart';
import 'device.dart';
import 'open_url.dart';

/// Checks for and installs app updates published in the admin panel.
///
/// Android: the APK is downloaded inside the app (with progress), its SHA-256
/// is verified against the server's value, then handed to the system
/// installer. iOS: apps can't install themselves, so the release link
/// (TestFlight / App Store / itms-services) is opened.
class Updater {
  static const _ch = MethodChannel('vpn_platform/update');

  static Future<PackageInfo> info() => PackageInfo.fromPlatform();

  static Future<AppUpdate?> check(ApiClient api) async {
    final platform = Device.platform;
    if (platform.isEmpty) return null;
    final build = int.tryParse((await info()).buildNumber) ?? 0;
    return api.appUpdate(platform, build);
  }

  /// Downloads and installs [u]. [onProgress] receives 0..1 (Android only).
  /// Throws [UpdateException] with a user-facing message.
  static Future<void> install(AppUpdate u, {void Function(double)? onProgress}) async {
    if (u.url.isEmpty) throw UpdateException('没有可用的下载地址');
    if (!Platform.isAndroid || !u.url.toLowerCase().contains('.apk')) {
      if (!await openUrl(Uri.parse(u.url))) throw UpdateException('无法打开下载链接');
      return;
    }
    final dir = Directory('${(await getTemporaryDirectory()).path}/updates')..createSync(recursive: true);
    // Remove leftovers from earlier updates.
    for (final f in dir.listSync()) {
      try {
        f.deleteSync();
      } catch (_) {}
    }
    final file = File('${dir.path}/huanyou-${u.build}.apk');
    final client = http.Client();
    try {
      final resp = await client.send(http.Request('GET', Uri.parse(u.url)));
      if (resp.statusCode != 200) throw UpdateException('下载失败（HTTP ${resp.statusCode}）');
      final total = resp.contentLength ?? u.size;
      final sink = file.openWrite();
      final digest = _DigestSink();
      final hasher = sha256.startChunkedConversion(digest);
      var got = 0;
      try {
        await for (final chunk in resp.stream) {
          sink.add(chunk);
          hasher.add(chunk);
          got += chunk.length;
          if (total > 0) onProgress?.call(got / total);
        }
      } finally {
        await sink.close();
        hasher.close();
      }
      if (u.sha256.isNotEmpty && digest.value.toString() != u.sha256.toLowerCase()) {
        file.deleteSync();
        throw UpdateException('安装包校验失败（文件不完整或被篡改），请重试');
      }
    } on SocketException {
      throw UpdateException('网络中断，下载失败');
    } on http.ClientException {
      throw UpdateException('网络中断，下载失败');
    } finally {
      client.close();
    }
    try {
      await _ch.invokeMethod('installApk', {'path': file.path});
    } on PlatformException catch (e) {
      throw UpdateException(e.message ?? '无法启动安装');
    }
  }
}

class UpdateException implements Exception {
  UpdateException(this.message);
  final String message;
  @override
  String toString() => message;
}

class _DigestSink implements Sink<Digest> {
  late Digest value;
  @override
  void add(Digest data) => value = data;
  @override
  void close() {}
}
