export function bytes(n: number | undefined | null): string {
  if (!n) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : v < 10 ? 2 : 1)} ${u[i]}`
}

export const yuan = (cents: number) => '¥' + (cents / 100).toFixed(cents % 100 === 0 ? 0 : 2)

const isZero = (s?: string | null) => !s || s.startsWith('0001')
const p2 = (x: number) => String(x).padStart(2, '0')

export function date(s: string | null | undefined): string {
  if (isZero(s)) return '—'
  const d = new Date(s!)
  return `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())}`
}

export function dateTime(s: string | null | undefined): string {
  if (isZero(s)) return '—'
  const d = new Date(s!)
  return `${date(s)} ${p2(d.getHours())}:${p2(d.getMinutes())}`
}

export function ago(s: string | null | undefined): string {
  if (isZero(s)) return '从未'
  const sec = Math.round((Date.now() - new Date(s!).getTime()) / 1000)
  if (sec < 60) return `${Math.max(sec, 0)} 秒前`
  if (sec < 3600) return `${Math.round(sec / 60)} 分钟前`
  if (sec < 86400) return `${Math.round(sec / 3600)} 小时前`
  return `${Math.round(sec / 86400)} 天前`
}

export const speed = (kbps: number) =>
  !kbps ? '不限速' : kbps >= 1000 ? `${+(kbps / 1000).toFixed(1)} Mbps` : `${kbps} kbps`

export const traffic = (gb: number) => (!gb ? '不限' : gb < 1 ? `${Math.round(gb * 1024)} MB` : `${gb} GB`)

const ISO = new Set(
  (
    'AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ CA CC CD ' +
    'CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR GA GB GD GE GF GG ' +
    'GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR ' +
    'KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF ' +
    'NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM ' +
    'SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS YE ' +
    'YT ZA ZM ZW'
  ).split(' '),
)

/** Country code → flag emoji ("US" → 🇺🇸); 🌐 for unknown codes. */
export function flag(code: string): string {
  const c = (code || '').toUpperCase()
  if (!ISO.has(c)) return '🌐'
  return String.fromCodePoint(...[...c].map((ch) => 0x1f1e6 + ch.charCodeAt(0) - 65))
}

/** Error → user message (ApiError carries the server's Chinese message). */
export const errMsg = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** "3 天后到期" / "已过期 2 天" for a node rental expiry. */
export function expiryText(iso: string | null | undefined): string {
  if (!iso) return '未设置'
  const ms = new Date(iso).getTime() - Date.now()
  const abs = Math.abs(ms) / 3600000
  const span = abs >= 48 ? `${Math.floor(abs / 24)} 天` : abs >= 1 ? `${Math.floor(abs)} 小时` : `${Math.max(1, Math.round(abs * 60))} 分钟`
  return ms <= 0 ? `已过期 ${span}` : `${span}后到期`
}

/** Severity of a node expiry: expired | soon (≤ 7 days) | ok | none. */
export function expiryLevel(iso: string | null | undefined): 'expired' | 'soon' | 'ok' | 'none' {
  if (!iso) return 'none'
  const ms = new Date(iso).getTime() - Date.now()
  return ms <= 0 ? 'expired' : ms <= 7 * 86400000 ? 'soon' : 'ok'
}
