import 'dart:io';

import 'package:flutter/material.dart';
import 'package:window_manager/window_manager.dart';
import '../core/config.dart';

/// Desktop window chrome merged into the app (Windows / macOS).
///
/// The native title bar is hidden and the app draws edge to edge. The title
/// bar height is added to MediaQuery's top padding, the same way a phone's
/// status bar is, so every page's own background (app bars, headers, the
/// splash) extends up under it and no separate title strip is visible. On top
/// of that strip sits a drag-to-move area and, on Windows, the minimise and
/// close buttons (macOS keeps its native traffic lights).
///
/// The window has one fixed size (phone-like): it cannot be resized,
/// maximised or made full screen.
class DesktopFrame extends StatelessWidget {
  const DesktopFrame({super.key, required this.child});
  final Widget child;

  static bool get enabled => Platform.isWindows || Platform.isMacOS;

  /// macOS: standard title bar height (traffic lights). Windows: caption buttons.
  static double get barHeight => Platform.isMacOS ? 28 : 32;

  /// The fixed window size (logical pixels). Keep in sync with
  /// macos/Runner/MainFlutterWindow.swift and windows/runner/main.cpp.
  static const size = Size(420, 820);

  /// Called before runApp: size the window, lock it and hide the native title
  /// bar before the first frame is shown.
  static Future<void> init() async {
    if (!enabled) return;
    await windowManager.ensureInitialized();
    final s = _fitted();
    final options = WindowOptions(
      size: s,
      minimumSize: s,
      maximumSize: s,
      center: true,
      title: AppConfig.appName,
      titleBarStyle: TitleBarStyle.hidden,
      windowButtonVisibility: true, // macOS traffic lights
    );
    await windowManager.waitUntilReadyToShow(options, () async {
      await windowManager.setResizable(false);
      await windowManager.setMaximizable(false);
      await windowManager.show();
      await windowManager.focus();
    });
  }

  /// [size], shortened on screens too low for it (e.g. 1366×768 laptops)
  /// so the window still fits above the taskbar / below the menu bar.
  static Size _fitted() {
    final displays = WidgetsBinding.instance.platformDispatcher.displays;
    if (displays.isEmpty) return size;
    final d = displays.first;
    final screenH = d.size.height / d.devicePixelRatio;
    if (screenH <= 0) return size;
    // Not clamp(560, screenH - 96): on screens under 656 px that range is
    // empty and clamp throws, and the window would never open.
    final room = screenH - 96;
    final h = room >= size.height ? size.height : (room < 480 ? 480.0 : room);
    return Size(size.width, h);
  }

  @override
  Widget build(BuildContext context) {
    if (!enabled) return child;
    final mq = MediaQuery.of(context);
    final h = barHeight;
    return Stack(children: [
      MediaQuery(
        data: mq.copyWith(
          padding: mq.padding.copyWith(top: mq.padding.top + h),
          viewPadding: mq.viewPadding.copyWith(top: mq.viewPadding.top + h),
        ),
        child: child,
      ),
      Positioned(
        top: 0,
        left: 0,
        right: 0,
        height: h,
        child: Row(children: [
          // Leaves room for the traffic lights, which handle their own clicks.
          if (Platform.isMacOS) const SizedBox(width: 72),
          // Drag only: DragToMoveArea would maximise on double-click.
          Expanded(
            child: GestureDetector(
              behavior: HitTestBehavior.translucent,
              onPanStart: (_) => windowManager.startDragging(),
              child: const SizedBox.expand(),
            ),
          ),
          if (Platform.isWindows) const _CaptionButtons(),
        ]),
      ),
    ]);
  }
}

class _CaptionButtons extends StatelessWidget {
  const _CaptionButtons();

  @override
  Widget build(BuildContext context) {
    final b = Theme.of(context).brightness;
    return Row(mainAxisSize: MainAxisSize.min, children: [
      WindowCaptionButton.minimize(brightness: b, onPressed: windowManager.minimize),
      WindowCaptionButton.close(brightness: b, onPressed: windowManager.close),
    ]);
  }
}
