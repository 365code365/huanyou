// Fetch wrapper for the admin API (/admin/api/*), same origin as the panel.
// - attaches the admin token
// - turns non-2xx responses into ApiError carrying the server's Chinese message
// - on 401 clears the token and notifies listeners (AuthGuard redirects to /login)

const TOKEN_KEY = 'vpn_admin_token'
const listeners = new Set<() => void>()

export const token = {
  get: () => (typeof window === 'undefined' ? null : localStorage.getItem(TOKEN_KEY)),
  set: (t: string) => localStorage.setItem(TOKEN_KEY, t),
  clear: () => {
    localStorage.removeItem(TOKEN_KEY)
    listeners.forEach((l) => l())
  },
  /** Subscribe to logout (token cleared). Returns an unsubscribe function. */
  onClear: (l: () => void) => {
    listeners.add(l)
    return () => {
      listeners.delete(l)
    }
  },
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message)
  }
}

export type Query = Record<string, string | number | undefined | null>

export async function request<T>(method: string, path: string, body?: unknown, query?: Query): Promise<T> {
  let url = '/admin/api' + path
  if (query) {
    const qs = new URLSearchParams()
    for (const [k, v] of Object.entries(query)) if (v !== undefined && v !== null && v !== '') qs.set(k, String(v))
    const s = qs.toString()
    if (s) url += '?' + s
  }
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const t = token.get()
  if (t) headers.Authorization = 'Bearer ' + t

  let res: Response
  try {
    res = await fetch(url, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })
  } catch {
    throw new ApiError(0, 'network', '无法连接服务器')
  }
  const data = await res.json().catch(() => ({}))
  if (res.status === 401 && path !== '/login') token.clear()
  if (!res.ok) throw new ApiError(res.status, data.error ?? 'http_' + res.status, data.message ?? `请求失败 (${res.status})`)
  return data as T
}

export const http = {
  get: <T,>(path: string, query?: Query) => request<T>('GET', path, undefined, query),
  post: <T,>(path: string, body: unknown = {}) => request<T>('POST', path, body),
  put: <T,>(path: string, body: unknown) => request<T>('PUT', path, body),
  patch: <T,>(path: string, body: unknown) => request<T>('PATCH', path, body),
  del: <T,>(path: string) => request<T>('DELETE', path),
}

/**
 * POST that reads an NDJSON response stream, calling onEvent per line.
 * Non-2xx responses (validation errors before streaming starts) throw ApiError.
 */
export async function stream<E>(path: string, body: unknown, onEvent: (e: E) => void): Promise<void> {
  const headers: Record<string, string> = { Accept: 'application/x-ndjson', 'Content-Type': 'application/json' }
  const t = token.get()
  if (t) headers.Authorization = 'Bearer ' + t
  let res: Response
  try {
    res = await fetch('/admin/api' + path, { method: 'POST', headers, body: JSON.stringify(body) })
  } catch {
    throw new ApiError(0, 'network', '无法连接服务器')
  }
  if (!res.ok || !res.body) {
    const data = await res.json().catch(() => ({}))
    if (res.status === 401) token.clear()
    throw new ApiError(res.status, data.error ?? 'http_' + res.status, data.message ?? `请求失败 (${res.status})`)
  }
  const reader = res.body.getReader()
  const dec = new TextDecoder()
  let buf = ''
  for (;;) {
    const { value, done } = await reader.read()
    if (done) break
    buf += dec.decode(value, { stream: true })
    let i
    while ((i = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, i).trim()
      buf = buf.slice(i + 1)
      if (line) onEvent(JSON.parse(line) as E)
    }
  }
  if (buf.trim()) onEvent(JSON.parse(buf) as E)
}
