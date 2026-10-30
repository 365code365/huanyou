// One function per admin endpoint. Pages import from here, never call fetch directly.
import { download, http, stream, upload } from './http'
import type {
  AIStatus,
  BackupInfo,
  LangList,
  LangPack,
  CloneEvent,
  CloneRequest,
  Release,
  Contact,
  GeoIP,
  ProvisionEvent,
  ProvisionReport,
  ProvisionRequest,
  AppRec,
  Dashboard,
  MonitorEvent,
  Order,
  Page,
  Plan,
  Settings,
  SystemInfo,
  User,
  UserDetail,
  VpnNode,
} from './types'

export * from './types'
export { ApiError, token } from './http'

/** Same CRUD shape for every catalog list on the server (plans / nodes / apps). */
export interface CatalogApi<T> {
  list: () => Promise<Page<T>>
  create: (item: T) => Promise<T>
  update: (item: T) => Promise<T>
  remove: (id: string) => Promise<unknown>
}

function catalog<T extends { id: string }>(path: string): CatalogApi<T> {
  return {
    list: () => http.get<Page<T>>(`/${path}`),
    create: (item) => http.post<T>(`/${path}`, item),
    update: (item) => http.put<T>(`/${path}/${item.id}`, item),
    remove: (id) => http.del<{ ok: true }>(`/${path}/${id}`),
  }
}

export interface ListQuery {
  q?: string
  status?: string
  page: number
  size: number
}

export type UserPatch = Partial<{ disabled: boolean; note: string; password: string; plan_id: string; expires_at: string }>

export const api = {
  login: (password: string) => http.post<{ token: string }>('/login', { password }),
  dashboard: () => http.get<Dashboard>('/dashboard'),

  users: {
    list: (q: ListQuery) => http.get<Page<User>>('/users', { ...q }),
    get: (id: number) => http.get<UserDetail>(`/users/${id}`),
    create: (body: { email: string; password: string; plan_id?: string; days?: number; note?: string }) =>
      http.post<User>('/users', body),
    patch: (id: number, body: UserPatch) => http.patch<User>(`/users/${id}`, body),
    remove: (id: number) => http.del<{ ok: true }>(`/users/${id}`),
    grant: (id: number, plan_id: string, days: number) => http.post<User>(`/users/${id}/grant`, { plan_id, days }),
    resetTraffic: (id: number) => http.post<User>(`/users/${id}/reset-traffic`),
    rotateUUID: (id: number) => http.post<User>(`/users/${id}/rotate-uuid`),
  },

  orders: {
    list: (q: ListQuery) => http.get<Page<Order>>('/orders', { ...q }),
    markPaid: (id: string) => http.post<{ granted: boolean }>(`/orders/${id}/mark-paid`),
  },

  plans: catalog<Plan>('plans'),
  apps: catalog<AppRec>('apps'),
  contacts: catalog<Contact>('contacts'),
  nodes: {
    ...catalog<VpnNode>('nodes'),
    install: (id: string) => http.get<{ node_json: string; steps: string }>(`/nodes/${id}/install`),
    rotateToken: (id: string) => http.post<{ token: string }>(`/nodes/${id}/rotate-token`),
    geoip: (ip: string) => http.get<GeoIP>('/geoip', { ip }),
    precheck: (body: ProvisionRequest) => http.post<ProvisionReport>('/nodes/precheck', body),
    upgrade: (id: string, body: { user?: string; port?: number; password?: string; private_key?: string }) =>
      http.post<{ ok: boolean; arch: string; log: string }>(`/nodes/${id}/upgrade`, body),
    provision: (body: ProvisionRequest, onEvent: (e: ProvisionEvent) => void) =>
      stream('/nodes/provision', body, onEvent),
  },

  releases: {
    list: () => http.get<Page<Release>>('/releases'),
    create: (form: FormData, onProgress?: (p: number) => void) => upload<Release>('/releases', form, onProgress),
    patch: (id: number, body: Partial<Pick<Release, 'notes' | 'force' | 'published'>>) => http.patch<Release>(`/releases/${id}`, body),
    remove: (id: number) => http.del<{ ok: true }>(`/releases/${id}`),
  },

  system: {
    backupInfo: () => http.get<BackupInfo>('/backup/info'),
    backup: (password: string, include_packages: boolean) => download('/backup', { password, include_packages }, 'huanyou-backup.hybk'),
    restore: (form: FormData, onProgress?: (p: number) => void) => upload<{ ok: boolean; message: string }>('/backup/restore', form, onProgress),
    clone: (body: CloneRequest, onEvent: (e: CloneEvent) => void) => stream('/clone', body, onEvent),
  },

  i18n: {
    list: () => http.get<LangList>('/i18n'),
    base: (code = '', name = '') => http.get<{ keys: string[]; prompt: string }>('/i18n/base', { code, name }),
    get: (code: string) => http.get<LangPack>(`/i18n/${encodeURIComponent(code)}`),
    save: (p: Pick<LangPack, 'code' | 'name' | 'native_name' | 'rtl' | 'enabled'> & { strings: Record<string, string> }) =>
      http.put<{ language: LangPack; placeholder_mismatch: string[] | null }>(`/i18n/${encodeURIComponent(p.code)}`, p),
    remove: (code: string) => http.del<unknown>(`/i18n/${encodeURIComponent(code)}`),
    parse: (text: string) => http.post<{ strings: Record<string, string>; placeholder_mismatch: string[] | null }>('/i18n/parse', { text }),
    translate: (code: string, name: string, keys: string[]) =>
      http.post<{ strings: Record<string, string> }>('/i18n/translate', { code, name, keys }),
    saveAI: (c: { base_url: string; model: string; api_key: string }) => http.put<AIStatus>('/i18n/ai', c),
  },

  settings: {
    get: () => http.get<{ settings: Settings; system: SystemInfo }>('/settings'),
    save: (s: Settings) => http.put<Settings>('/settings', s),
  },

  monitor: {
    events: () => http.get<{ items: MonitorEvent[] }>('/monitor/events'),
    checkNow: () => http.post<{ items: VpnNode[] }>('/monitor/check'),
    testAlert: () => http.post<{ ok: true }>('/monitor/test-alert'),
  },
}
