import 'package:flutter/widgets.dart';

import 'app_controller.dart';

/// Exposes the AppController to the widget tree and rebuilds dependents on change.
///
///   final app = AppScope.of(context);       // listen (rebuilds on change)
///   AppScope.read(context).connect();        // one-off call, no rebuild
class AppScope extends InheritedNotifier<AppController> {
  const AppScope({super.key, required AppController controller, required super.child})
      : super(notifier: controller);

  static AppController of(BuildContext context) =>
      context.dependOnInheritedWidgetOfExactType<AppScope>()!.notifier!;

  static AppController read(BuildContext context) =>
      context.getInheritedWidgetOfExactType<AppScope>()!.notifier!;
}
