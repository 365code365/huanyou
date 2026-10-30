// Mirrors the Go JSON shapes in server/internal/{config,api,store,monitor}.
// Keep in sync when changing server responses.

export interface Page<T> {
  items: T[]
  total: number
}

export interface Plan {
  id: string
  name: string
  description: string
  price_cents: number
  duration_days: number
  traffic_gb: number
  speed_mbps: number
  over_quota_kbps: number
  hidden: boolean
  sort: number
  hot: boolean // 🔥 HOT badge in the app
  free?: boolean // response only
}

export interface Reality {
  public_key: string
  short_id: string
  server_name: string
  fingerprint: string
}

export interface Probe {
  at: string
  ok: boolean
  latency_ms: number
}

export interface NodeStatus {
  online: boolean
  latency_ms: number
  uptime: number
  history: Probe[] | null
  last_check: string
  agent_online: boolean
  agent_seen: string
  users: number
  active_users: number
  traffic_bytes: number
}

export interface VpnNode {
  id: string
  name: string
  region: string
  address: string
  token: string // masked in responses
  reality: Reality
  reality_target: string
  vision_port: number
  xhttp_path: string
  disabled: boolean // 下架: hidden from the app
  frozen: boolean // 冻结: serves nobody, sessions dropped
  frozen_reason: string
  note: string
  tags: string[]
  rate: number // 流量倍率
  plans: string[] // allowed plan ids, empty = all plans
  bandwidth_mbps: number // fair-share capacity, 0 = off
  weight: number
  sort: number
  expires_at?: string | null // server rental expiry (admin only)
  vendor: string
  vendor_url: string
  status?: NodeStatus // response only
}

export interface AppRec {
  id: string
  name: string
  icon_url: string
  url: string
  category: string
  desc: string
  disabled: boolean
  sort: number
}

export interface Entitlement {
  plan_id: string
  plan_name: string
  paid: boolean
  expires_at: string | null
  quota_bytes: number
  used_bytes: number
  period_end: string
  speed_kbps: number
  throttled: boolean
  can_connect: boolean
  reason?: string
}

export interface User {
  id: number
  email: string // "" for device accounts
  recharge_code: string // e.g. VP-00010
  device_id: string
  platform: string // ios | android | ""
  disabled: boolean
  note: string
  ref_code: string
  invited_by: number
  uuid: string
  created_at: string
  plan_id: string
  expires_at: string | null
  entitlement: Entitlement
  tier: string // segment value tier key, see Segment
  tags: string[] // segment behaviour tag keys
  blocked: string // risk-control block reason
  is_blocked: boolean
  blocked_by: number // user id carrying the block (self or multi-device owner)
}

export interface Order {
  id: string
  plan_id: string
  amount_cents: number
  method: string
  status: 'pending' | 'paid'
  created_at: string
  paid_at: string | null
  email: string
  user_id: number
  payment_no: string // provider trade no; MANUAL = admin mark-paid
}

export interface DailyUsage {
  day: string
  up: number
  down: number
}

export interface UserDetail {
  user: User
  usage: DailyUsage[]
  orders: Order[]
  invited_count: number
  ips: IPRecord[]
  ip_total: number
}

export interface ReferralSettings {
  enabled: boolean
  reward_plan_id: string
  inviter_days: number
  invitee_days: number
  max_invites: number
}

export interface MonitorSettings {
  enabled: boolean
  interval_seconds: number
  timeout_seconds: number
  fail_threshold: number
  alert_webhook: string
  alert_format: 'generic' | 'feishu' | 'dingtalk' | 'wecom'
}

export interface Settings {
  free_plan: string
  traffic_period_days: number
  referral: ReferralSettings
  monitor: MonitorSettings
  ai_domains: string[]
  share: ShareSettings
  multi_device: { max_devices: number; min_plan_days: number }
}

export interface SystemInfo {
  public_url: string
  allow_register: boolean
  payment_methods: string[]
  mock_payments: boolean
}

export interface MonitorEvent {
  at: string
  node_id: string
  level: 'error' | 'warning' | 'info'
  text: string
}

export interface Stats {
  users: number
  new_today: number
  paid_active: number
  disabled: number
  orders: number
  revenue_cents: number
  revenue_today_cents: number
  traffic_today_bytes: number
  active_today: number
}

export interface TrendPoint {
  day: string
  new_users: number
  revenue_cents: number
  traffic_bytes: number
}

export interface Dashboard {
  stats: Stats
  trend: TrendPoint[]
  nodes: VpnNode[]
  nodes_online: number
  events: MonitorEvent[]
  expiring_nodes: ExpiringNode[]
}

export interface GeoIP {
  ip: string
  country_code: string
  country: string
  region: string
  city: string
  suggested_name: string
}

export interface ProvisionRequest {
  host: string
  port?: number
  user?: string
  password?: string
  private_key?: string
  node_port?: number
  name?: string
  region?: string
  server_name?: string
  expires_at?: string
  vendor?: string
  vendor_url?: string
}

export interface ProvisionReport {
  arch: string
  os: string
  systemd: boolean
  port: number
  port_free: boolean
  port_user: string
  suggest_port: number
  ram_mb: number
  disk_free_gb: number
  time_synced: boolean
  pkg_mgr: string
  virt: string
  arch_ok: boolean
  fatal: string[] | null
  warn: string[] | null
}

export type ProvisionEvent =
  | { type: 'log'; text: string }
  | { type: 'error'; message: string }
  | { type: 'done'; node: VpnNode; arch: string }

export interface ShareSettings {
  android_url: string
  ios_url: string
  slogan: string
}

export interface Contact {
  id: string
  type: string
  group: 'service' | 'social'
  name: string
  value: string
  url: string
  desc: string
  disabled: boolean
  sort: number
  link?: string // response-only: resolved URL ("" = app copies value)
}

export interface Release {
  id: number
  platform: 'android' | 'ios'
  version: string
  build: number
  notes: string
  file: string
  url: string
  size: number
  sha256: string
  bundle_id: string
  force: boolean
  published: boolean
  abi: string // Android CPU type: arm64-v8a | armeabi-v7a | x86_64 | "" (universal)
  created_at: string
  download_url: string
}

export interface BackupInfo {
  db_bytes: number
  package_bytes: number
  packages: number
  users: number
  orders: number
  nodes: number
  public_url: string
}

export interface CloneRequest extends ProvisionRequest {
  domain: string
  email?: string
  admin_password: string
  mode: 'full' | 'config'
  keep_nodes: boolean
}

export type CloneEvent =
  | { type: 'log'; text: string }
  | { type: 'error'; message: string }
  | { type: 'done'; url: string; admin: string; tls: boolean; migrate?: boolean; ip?: string }
export interface LangPack {
  code: string
  name: string
  native_name: string
  rtl: boolean
  enabled: boolean
  version: number
  count: number
  strings?: Record<string, string>
  updated_at: string
}
export interface SuggestedLang {
  code: string
  name: string
  native_name: string
  rtl: boolean
  region: string
}
export interface AIStatus {
  ready: boolean
  base_url: string
  model: string
  api_key: string
}
export interface LangList {
  languages: LangPack[]
  base_count: number
  suggested: SuggestedLang[]
  ai: AIStatus
}

// ---- risk control ----

export interface IPRecord {
  user_id: number
  recharge_code: string
  ip: string
  source: 'api' | 'node'
  node_id: string
  node_name: string
  hits: number
  first_seen: string
  last_seen: string
  class: 'cn' | 'abroad' | 'private' | ''
  blocked: boolean
}

export interface RiskConfig {
  record_api: boolean | null
  record_node: boolean | null
  retention_days: number
  max_ips_per_user: number
  max_users_per_ip: number
}

export interface SuspectUser {
  user_id: number
  recharge_code: string
  ips: number
  nodes: number
  last_seen: string
  plan_id: string
  expires_at: string | null
  blocked: boolean
  disabled: boolean
}

export interface SuspectIP {
  ip: string
  users: number
  hits: number
  last_seen: string
  user_ids: number[]
  recharge_codes: string[]
  class: string
  blocked: boolean
}

export interface Suspicious {
  hours: number
  multi_ip_users: SuspectUser[]
  shared_ips: SuspectIP[]
  config: RiskConfig
}

export interface RiskRule {
  kind: 'ip' | 'user'
  value: string
  reason: string
  expires_at: string | null
  created_at: string
  updated_at: string
  active: boolean
  recharge_code?: string
}

export interface GeoLocation {
  ip: string
  country: string
  country_code: string
  region: string
  city: string
}

// ---- segmentation ----

export interface SegmentStat {
  key: string
  label: string
  group: 'tier' | 'tag'
  color: string
  desc: string
  advice: string
  users: number
  share: number
  revenue_cents: number
  avg_ltv_cents: number
  paying_users: number
  avg_bytes_30: number
  active_share: number
}

export interface SegmentConfig {
  high_value_cents: number
  low_value_cents: number
  loyal_orders: number
  active_days: number
  promoter_invites: number
  churn_days: number
}

export interface SegmentReport {
  generated_at: string
  totals: {
    users: number
    accounts: number
    paying: number
    paid_active: number
    conversion: number
    repurchase_rate: number
    arppu_cents: number
    revenue_cents: number
    revenue_30_cents: number
    active_7: number
  }
  tiers: SegmentStat[]
  tags: SegmentStat[]
  plan_mix: { plan_id: string; name: string; orders: number; revenue_cents: number; buyers: number }[]
  config: SegmentConfig
}

export interface SegmentUser {
  id: number
  recharge_code: string
  tier: string
  tags: string[]
  plan_id: string
  expires_at: string | null
  platform: string
  created_at: string
  ltv_cents: number
  paid_orders: number
  max_order_cents: number
  last_paid_at: string | null
  bytes_30: number
  active_days_30: number
  last_active_day: string
  invites: number
}

// ---- database ----

export type DBDriver = 'sqlite' | 'mysql' | 'postgres'

export interface TableResult {
  table: string
  upserted: number
  deleted: number
}

export interface DBInfo {
  driver: DBDriver
  dsn: string
  schema_version: number
  counts: Record<string, number>
  ping_ms: number
  ping_error?: string
  sqlite_path: string
  env_override: boolean
  marks: Record<string, { at: number; full: boolean; seconds: number; result: TableResult[] }>
}

export type DBSyncEvent =
  | { type: 'start'; full: boolean; since: number }
  | { type: 'progress'; table: string; rows: number }
  | { type: 'done'; result: { started_at: number; tables: TableResult[]; seconds: number } }
  | { type: 'error'; message: string }

// ---- settings sections ----

export interface GatewaySettings {
  enabled: boolean
  name: string
  pay_link: string
  merchant_id: string
  sign_key: string
  enc_key: string
  allow_ips: string[]
}

export interface PaymentSettings {
  disabled: boolean
  disabled_notice: string
  gateway: GatewaySettings
}

export interface Ad {
  id: string
  slot: 'home' | 'popup'
  title: string
  text: string
  image: string
  link: string
  enabled: boolean
  sort: number
  start_at?: string | null
  end_at?: string | null
}

export interface AdsSettings {
  home_enabled: boolean
  home_interval: number
  popup_enabled: boolean
  popup_frequency: 'every_launch' | 'daily' | 'once'
  items: Ad[]
}

export interface GuardSettings {
  disabled: boolean
  rate_per_minute: number
  errors_per_10m: number
  ban_minutes: number
  allow_ips: string[]
}

export interface AutoBan {
  ip: string
  until: string
  reason: string
  path: string
  bad: number
}

export interface ExpiringNode {
  id: string
  name: string
  address: string
  expires_at: string
  vendor: string
  vendor_url: string
  expired: boolean
  hours_left: number
}

// ---- analytics ----

export interface DayStat {
  day: string
  uv: number
  opens: number
  new_users: number
  connects: number
  purchases: number
}

export interface Analytics {
  daily: DayStat[]
  today_uv: number
  uv_7: number
  uv_30: number
  events: { event: string; count: number; users: number }[]
  platforms: { key: string; users: number }[]
  versions: { key: string; users: number }[]
  crash_groups_7: number
  crash_reports_7: number
}

export interface Crash {
  id: string
  user_id: number
  platform: string
  app_version: string
  device: string
  os_version: string
  kind: string
  message: string
  stack?: string
  count: number
  users: number
  first_at: string
  last_at: string
  resolved: boolean
}
