String formatBytes(int b) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  var v = b.toDouble();
  var i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return '${v < 10 && i > 0 ? v.toStringAsFixed(2) : v.toStringAsFixed(v < 100 && i > 0 ? 1 : 0)} ${units[i]}';
}

String formatSpeed(int kbps) {
  if (kbps <= 0) return '不限速';
  if (kbps >= 1000) return '${(kbps / 1000).toStringAsFixed(kbps % 1000 == 0 ? 0 : 1)} Mbps';
  return '$kbps kbps';
}

String formatYuan(int cents) => '¥${(cents / 100).toStringAsFixed(cents % 100 == 0 ? 0 : 2)}';

String formatDate(DateTime d) =>
    '${d.year}-${d.month.toString().padLeft(2, '0')}-${d.day.toString().padLeft(2, '0')}';

String formatTraffic(double gb) {
  if (gb <= 0) return '不限流量';
  if (gb < 1) return '${(gb * 1024).round()} MB';
  return '${gb % 1 == 0 ? gb.toInt() : gb} GB';
}
