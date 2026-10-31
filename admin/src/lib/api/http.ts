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
    /** The whole error body, for endpoints that send more than error + message. */
    public data: Record<string, unknown> = {},
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
  if (!res.ok) throw new ApiError(res.status, data.error ?? 'http_' + res.status, data.message ?? `请求失败 (${res.status})`, data)
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

/**
 * multipart POST with upload progress (fetch can't report upload progress).
 * onProgress receives 0..1. Resolves with the JSON body; rejects with ApiError.
 */
export function upload<T>(path: string, form: FormData, onProgress?: (p: number) => void): Promise<T> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', '/admin/api' + path)
    const t = token.get()
    if (t) xhr.setRequestHeader('Authorization', 'Bearer ' + t)
    xhr.upload.onprogress = (e) => e.lengthComputable && onProgress?.(e.loaded / e.total)
    xhr.onerror = () => reject(new ApiError(0, 'network', '上传失败：网络中断'))
    xhr.onload = () => {
      let data: { error?: string; message?: string } = {}
      try {
        data = JSON.parse(xhr.responseText)
      } catch {
        // non-JSON (e.g. nginx 413 page)
      }
      if (xhr.status === 401) token.clear()
      if (xhr.status >= 200 && xhr.status < 300) return resolve(data as T)
      const msg = xhr.status === 413 ? '文件太大，被服务器拒绝（请检查 nginx client_max_body_size）' : data.message
      reject(new ApiError(xhr.status, data.error ?? 'http_' + xhr.status, msg ?? `上传失败 (${xhr.status})`))
    }
    xhr.send(form)
  })
}

/** POST JSON and save the binary response as a file (e.g. a backup). */
export async function download(path: string, body: unknown, fallbackName: string): Promise<void> {
  const t = token.get()
  const res = await fetch('/admin/api' + path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...(t ? { Authorization: 'Bearer ' + t } : {}) },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const data = await res.json().catch(() => ({}))
    if (res.status === 401) token.clear()
    throw new ApiError(res.status, data.error ?? 'http_' + res.status, data.message ?? `请求失败 (${res.status})`)
  }
  const name = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ?? fallbackName
  const url = URL.createObjectURL(await res.blob())
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 10000)
}

/** GET a file (e.g. a CSV export) with the admin token and save it. */
export async function downloadGet(path: string, fallbackName: string): Promise<void> {
  const t = token.get()
  const res = await fetch('/admin/api' + path, { headers: t ? { Authorization: 'Bearer ' + t } : {} })
  if (!res.ok) {
    const data = await res.json().catch(() => ({}))
    if (res.status === 401) token.clear()
    throw new ApiError(res.status, data.error ?? 'http_' + res.status, data.message ?? `请求失败 (${res.status})`)
  }
  const name = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ?? fallbackName
  const url = URL.createObjectURL(await res.blob())
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 10000)
}
