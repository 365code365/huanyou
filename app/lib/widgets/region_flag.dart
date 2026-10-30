import 'package:flutter/material.dart';

import '../theme/app_theme.dart';

// ISO 3166-1 alpha-2 codes that have a flag emoji.
const _iso = 'AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS '
    'BT BV BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES '
    'ET FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM '
    'IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME '
    'MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG '
    'PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY '
    'SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS YE YT ZA ZM ZW';
final _isoSet = _iso.split(' ').toSet();

/// Region code → flag emoji ("US" → 🇺🇸); 🌐 for unknown codes.
String flagEmoji(String code) {
  final c = code.toUpperCase();
  if (!_isoSet.contains(c)) return '🌐';
  return String.fromCharCodes(c.codeUnits.map((u) => 0x1F1E6 + u - 65));
}

/// Round flag badge used in node lists and the home node card.
class RegionFlag extends StatelessWidget {
  const RegionFlag(this.region, {super.key, this.size = 40});
  final String region;
  final double size;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: size,
      height: size,
      decoration: const BoxDecoration(color: AppColors.sunken, shape: BoxShape.circle),
      alignment: Alignment.center,
      child: Text(flagEmoji(region), style: TextStyle(fontSize: size * 0.55)),
    );
  }
}

/// Signal-strength style latency indicator.
class LatencyBadge extends StatelessWidget {
  const LatencyBadge({super.key, required this.online, required this.latencyMs});
  final bool online;
  final int latencyMs;

  @override
  Widget build(BuildContext context) {
    final (color, text) = !online
        ? (AppColors.danger, '离线')
        : latencyMs <= 0
            ? (AppColors.success, '在线')
            : latencyMs < 150
                ? (AppColors.success, '${latencyMs}ms')
                : latencyMs < 300
                    ? (AppColors.warning, '${latencyMs}ms')
                    : (AppColors.danger, '${latencyMs}ms');
    return Row(mainAxisSize: MainAxisSize.min, children: [
      Container(width: 7, height: 7, decoration: BoxDecoration(color: color, shape: BoxShape.circle)),
      const SizedBox(width: 5),
      Text(text, style: TextStyle(fontSize: 12, color: color, fontWeight: FontWeight.w500)),
    ]);
  }
}
