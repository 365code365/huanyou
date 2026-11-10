import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:crypto/crypto.dart';
import 'package:flutter/foundation.dart';
import 'package:http/http.dart' as http;

import '../i18n/i18n.dart';
import 'api.dart';
import 'updater.dart';
import 'vpn.dart';

/// In-app update for Windows and macOS: downloads the release (resuming after
/// dropped connections), checks its SHA-256, swaps the installed app for the
/// new one and restarts.
///
/// The swap runs in a small detached script after this process exits, since a
/// running app can't replace itself:
///   macOS: the DMG is mounted, its 环游.app copied out, and the script moves
///          it over the current bundle (e.g. /Applications/环游.app).
///   Windows: the zip is expanded and the script robocopies it over the folder
///          Huanyou.exe runs from; an Inno Setup installer (.exe) is run
///          silently into that folder instead (it asks for admin rights
///          itself when the folder needs them).
class DesktopUpdater {
  static bool get supported => Platform.isMacOS || Platform.isWindows;

  /// Tests point the installed app at a scratch copy.
  @visibleForTesting
  static String? installPathForTest;

  /// Where the running app is installed: the .app bundle (macOS) or the
  /// folder holding Huanyou.exe (Windows).
  static Directory get installDir {
    if (installPathForTest != null) return Directory(installPathForTest!);
    final exe = File(Platform.resolvedExecutable);
    return Platform.isMacOS ? exe.parent.parent.parent : exe.parent;
  }

  /// Why the app can't update itself in place ("" = it can). Users then
  /// download from the browser instead.
  static String blocker([AppUpdate? u]) {
    // Without a checksum (a release that only links an external URL) the
    // package can't be verified, so it is not installed in place.
    if (u != null && u.sha256.isEmpty) return tr('该版本需要在浏览器中下载安装');
    final dir = installDir;
    if (Platform.isMacOS) {
      final p = dir.path;
      if (p.startsWith('/Volumes/') || p.contains('/AppTranslocation/')) {
        return tr('请先把「环游」拖到「应用程序」文件夹，从那里打开后再更新');
      }
    }
    // A Windows installer elevates through UAC when the folder (e.g. Program
    // Files) needs it, so no write probe.
    if (Platform.isWindows && u?.kind == 'exe') return '';
    // The swap writes next to the installed app (the bundle's parent on macOS).
    final target = Platform.isMacOS ? dir.parent : dir;
    try {
      final probe = File('${target.path}${Platform.pathSeparator}.huanyou-update-probe');
      probe.writeAsStringSync('');
      probe.deleteSync();
      return '';
    } catch (_) {
      return tr('没有写入安装目录的权限，请用浏览器下载新版本手动安装');
    }
  }

  static Directory get _work => Directory('${Directory.systemTemp.path}${Platform.pathSeparator}huanyou-update');

  /// Downloads [u] into a temp folder and verifies it. [onProgress] gets 0..1.
  static Future<File> download(AppUpdate u, void Function(double) onProgress) async {
    final uri = Uri.tryParse(u.url);
    final local = uri != null && uri.scheme == 'http' && (uri.host == '127.0.0.1' || uri.host == 'localhost'); // dev API
    if (uri == null || !(uri.scheme == 'https' || local)) throw UpdateException(tr('没有可用的下载地址'));
    final dir = _work;
    if (dir.existsSync()) dir.deleteSync(recursive: true);
    dir.createSync(recursive: true);
    // A fixed name with the right extension: the URL may have none (a signed
    // or query URL), and Expand-Archive refuses files not ending in .zip.
    final name = Platform.isMacOS ? 'update.dmg' : (u.kind == 'exe' ? 'huanyou-setup.exe' : 'update.zip');
    final file = File('${dir.path}${Platform.pathSeparator}$name');
    final sink = file.openSync(mode: FileMode.write);
    final client = http.Client();
    var got = 0;
    var total = u.size;
    try {
      // The server may cut long transfers short; resume with Range requests.
      for (var attempt = 0; ; attempt++) {
        try {
          final req = http.Request('GET', Uri.parse(u.url));
          if (got > 0) req.headers['Range'] = 'bytes=$got-';
          final res = await client.send(req).timeout(const Duration(seconds: 30));
          if (got > 0 && res.statusCode == 200) {
            got = 0; // range ignored: start over
            sink.setPositionSync(0);
            sink.truncateSync(0);
          } else if (res.statusCode != 200 && res.statusCode != 206) {
            throw UpdateException(tr('下载失败（HTTP {0}）', [res.statusCode]));
          }
          if (total <= 0 && res.contentLength != null) total = got + res.contentLength!;
          await for (final chunk in res.stream.timeout(const Duration(seconds: 60))) {
            sink.writeFromSync(chunk);
            got += chunk.length;
            if (total > 0) onProgress(got / total);
          }
          if (total <= 0 || got >= total) break;
        } on UpdateException {
          rethrow;
        } catch (_) {
          // dropped connection / timeout: retry below
        }
        if (attempt >= 8) throw UpdateException(tr('下载中断，请检查网络后重试'));
        await Future<void>.delayed(Duration(seconds: 1 + attempt));
      }
    } finally {
      sink.closeSync();
      client.close();
    }
    if (u.sha256.isEmpty || await _sha256(file) != u.sha256.toLowerCase()) {
      file.deleteSync();
      throw UpdateException(tr('安装包校验失败，请重试'));
    }
    return file;
  }

  static Future<String> _sha256(File f) async => (await sha256.bind(f.openRead()).first).toString();

  /// Prepares the new app from [pkg], starts the swap script, disconnects the
  /// VPN (restoring the system proxy) and quits. The script relaunches the app.
  static Future<Never> installAndRestart(File pkg) async {
    final script = await prepare(pkg);
    try {
      await Vpn.disconnect().timeout(const Duration(seconds: 10));
    } catch (_) {}
    if (Platform.isMacOS) {
      await Process.start('/bin/sh', [script], mode: ProcessStartMode.detached);
    } else {
      await Process.start(
        'powershell.exe',
        ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-WindowStyle', 'Hidden', '-File', script],
        mode: ProcessStartMode.detached,
      );
    }
    exit(0);
  }

  /// Unpacks the new version next to [pkg] and writes the swap script, which
  /// waits for process [waitPid] (default: this app) to exit. Returns its path.
  @visibleForTesting
  static Future<String> prepare(File pkg, {int? waitPid}) => Platform.isMacOS
      ? _prepareMac(pkg, waitPid ?? pid)
      : pkg.path.toLowerCase().endsWith('.exe')
          ? _prepareWindowsInstaller(pkg, waitPid ?? pid)
          : _prepareWindows(pkg, waitPid ?? pid);

  // ---- macOS ----

  static Future<String> _prepareMac(File dmg, int waitPid) async {
    final work = dmg.parent.path;
    final mnt = '$work/mnt';
    final staged = '$work/new.app';
    Directory(mnt).createSync(recursive: true);
    final att = await Process.run('/usr/bin/hdiutil', ['attach', '-nobrowse', '-readonly', '-noautoopen', '-mountpoint', mnt, dmg.path]);
    if (att.exitCode != 0) throw UpdateException(tr('无法打开安装包：{0}', ['${att.stderr}'.trim()]));
    try {
      final app = Directory(mnt).listSync().whereType<Directory>().where((d) => d.path.endsWith('.app')).firstOrNull;
      if (app == null) throw UpdateException(tr('安装包里没有 App'));
      final cp = await Process.run('/usr/bin/ditto', [app.path, staged]);
      if (cp.exitCode != 0) throw UpdateException(tr('复制新版本失败：{0}', ['${cp.stderr}'.trim()]));
    } finally {
      await Process.run('/usr/bin/hdiutil', ['detach', mnt, '-force']);
    }
    final target = installDir.path;
    final q = _shQuote;
    final script = '$work/install.sh';
    // Wait for this process, swap the bundle (keeping the old one until the
    // copy succeeded), relaunch, clean up.
    File(script).writeAsStringSync('''#!/bin/sh
while kill -0 $waitPid 2>/dev/null; do sleep 0.2; done
sleep 0.5
T=${q(target)}
if mv "\$T" "\$T.old" && /usr/bin/ditto ${q(staged)} "\$T"; then
  rm -rf "\$T.old"
else
  rm -rf "\$T"; mv "\$T.old" "\$T"
fi
/usr/bin/open -n "\$T"
rm -rf ${q(work)}
''');
    return script;
  }

  static String _shQuote(String s) => "'${s.replaceAll("'", r"'\''")}'";

  // ---- Windows ----

  static Future<String> _prepareWindows(File zip, int waitPid) async {
    final work = zip.parent.path;
    final out = '$work\\new';
    final ps = _psQuote;
    final ex = await Process.run('powershell.exe', [
      '-NoProfile', '-NonInteractive', '-Command',
      'Expand-Archive -LiteralPath ${ps(zip.path)} -DestinationPath ${ps(out)} -Force',
    ]);
    if (ex.exitCode != 0) throw UpdateException(tr('无法解压安装包：{0}', ['${ex.stderr}'.trim()]));
    final exe = Directory(out)
        .listSync(recursive: true)
        .whereType<File>()
        .where((f) => f.path.toLowerCase().endsWith('\\huanyou.exe'))
        .fold<File?>(null, (best, f) => best == null || f.path.length < best.path.length ? f : best);
    if (exe == null) throw UpdateException(tr('安装包里没有 Huanyou.exe'));
    final src = exe.parent.path;
    final dst = installDir.path;
    final script = '$work\\install.ps1';
    // Wait for this process and its VPN core, copy the new files over the old
    // ones, relaunch, clean up.
    final body = '''
\$ErrorActionPreference = 'Continue'
try { Wait-Process -Id $waitPid -Timeout 60 -ErrorAction SilentlyContinue } catch {}
Get-Process -Name huanyou-core -ErrorAction SilentlyContinue | Where-Object { \$_.Path -like ${ps('$dst\\*')} } | Wait-Process -Timeout 15 -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 500
robocopy ${ps(src)} ${ps(dst)} /E /R:10 /W:1 /NFL /NDL /NJH /NJS /NP | Out-Null
Start-Process -FilePath ${ps('$dst\\Huanyou.exe')}
Remove-Item -LiteralPath ${ps(work)} -Recurse -Force -ErrorAction SilentlyContinue
''';
    // UTF-8 with BOM so Windows PowerShell 5 reads non-ASCII paths (环游) correctly.
    File(script).writeAsBytesSync([0xEF, 0xBB, 0xBF, ...utf8.encode(body)]);
    return script;
  }

  /// Runs an Inno Setup installer silently into the current install folder
  /// (keeps a zip install where it is, and an installed copy in place), then
  /// relaunches. If the user declines the admin prompt or setup fails, the
  /// old version still starts.
  static Future<String> _prepareWindowsInstaller(File setup, int waitPid) async {
    final work = setup.parent.path;
    final dst = installDir.path;
    final ps = _psQuote;
    final args = '/VERYSILENT /SUPPRESSMSGBOXES /NORESTART /SP- /CLOSEAPPLICATIONS /DIR="$dst"';
    final script = '$work\\install.ps1';
    final body = '''
\$ErrorActionPreference = 'Continue'
try { Wait-Process -Id $waitPid -Timeout 60 -ErrorAction SilentlyContinue } catch {}
Get-Process -Name huanyou-core -ErrorAction SilentlyContinue | Where-Object { \$_.Path -like ${ps('$dst\\*')} } | Wait-Process -Timeout 15 -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 500
try { Start-Process -FilePath ${ps(setup.path)} -ArgumentList ${ps(args)} -Wait } catch {}
if (-not (Get-Process -Name Huanyou -ErrorAction SilentlyContinue)) {
  \$exe = ${ps('$dst\\Huanyou.exe')}
  if (Test-Path -LiteralPath \$exe) { Start-Process -FilePath \$exe }
}
Remove-Item -LiteralPath ${ps(work)} -Recurse -Force -ErrorAction SilentlyContinue
''';
    File(script).writeAsBytesSync([0xEF, 0xBB, 0xBF, ...utf8.encode(body)]);
    return script;
  }

  static String _psQuote(String s) => "'${s.replaceAll("'", "''")}'";
}
