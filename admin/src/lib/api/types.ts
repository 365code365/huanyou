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
  disabled: boolean
  weight: number
  sort: number
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
