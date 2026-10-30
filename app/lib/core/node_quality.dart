import 'dart:async';
import 'dart:io';

import 'api.dart';
import '../i18n/i18n.dart';

/// Quality of one node as seen from this device.
class NodeQuality {
  const NodeQuality({required this.latencyMs, required this.loss});

  /// Median TCP connect time in ms, or null if every attempt failed.
  final int? latencyMs;

  /// Share of failed attempts (0..1).
  final double loss;

  bool get reachable => latencyMs != null;
}

enum QualityLevel {
  excellent('极佳', 4),
  good('良好', 3),
  fair('一般', 2),
  poor('较差', 1),
  down('不可用', 0),
  unknown('待测', 0);

  const QualityLevel(this._label, this.bars);
  final String _label;
  String get label => tr(_label);
  final int bars; // signal bars, 0..4
}

/// Combines the device's own measurement with server-side health into one
/// comparable score (lower is better) and a level for display.
class NodeScore {
  NodeScore(this.node, this.quality);
  final Node node;
  final NodeQuality? quality;

  /// Effective latency: penalties for loss, instability and load are added
  /// in milliseconds, so the score reads like "how slow this node feels".
  double get value {
    if (!node.online) return double.infinity;
    final q = quality;
    if (q == null) return 10000 + node.latencyMs.toDouble(); // unmeasured: after measured ones
    if (!q.reachable) return double.infinity;
    return q.latencyMs! + q.loss * 400 + (1 - node.uptime).clamp(0, 1) * 600 + node.load * 4;
  }

  QualityLevel get level {
    if (!node.online || (quality != null && !quality!.reachable)) return QualityLevel.down;
    if (quality == null) return QualityLevel.unknown;
    final v = value;
    if (v <= 150) return QualityLevel.excellent;
    if (v <= 260) return QualityLevel.good;
    if (v <= 450) return QualityLevel.fair;
    return QualityLevel.poor;
  }

  String get loadLabel => node.load <= 5 ? tr('空闲') : node.load <= 30 ? tr('适中') : tr('繁忙');
}

/// Measures TCP connect latency to each node directly from this device. The
/// app's own sockets bypass the VPN tunnel, so this is the user's real path
/// to the node even while connected.
class NodeProber {
  static const attempts = 3;
  static const timeout = Duration(milliseconds: 2500);

  static Future<NodeQuality> probe(Node n) async {
    if (n.host.isEmpty) return const NodeQuality(latencyMs: null, loss: 1);
    final times = <int>[];
    for (var i = 0; i < attempts; i++) {
      final sw = Stopwatch()..start();
      try {
        final s = await Socket.connect(n.host, n.port, timeout: timeout);
        sw.stop();
        s.destroy();
        times.add(sw.elapsedMilliseconds);
      } catch (_) {
        // counted as loss
      }
    }
    if (times.isEmpty) return const NodeQuality(latencyMs: null, loss: 1);
    times.sort();
    return NodeQuality(latencyMs: times[times.length ~/ 2], loss: 1 - times.length / attempts);
  }

  /// Probes all nodes, at most [parallel] at a time, reporting each result as it lands.
  static Future<void> probeAll(List<Node> nodes, void Function(String id, NodeQuality q) onResult,
      {int parallel = 6}) async {
    final queue = [...nodes];
    Future<void> worker() async {
      while (queue.isNotEmpty) {
        final n = queue.removeLast();
        onResult(n.id, await probe(n));
      }
    }

    await Future.wait([for (var i = 0; i < parallel; i++) worker()]);
  }
}
