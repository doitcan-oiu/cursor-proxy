/**
 * 管理 API 客户端。所有请求带 ADMIN_TOKEN，401 会通过 onUnauthorized 回调通知上层登出。
 */

export interface ServerInfo {
  host: string
  port: number
  baseUrl: string
  adminToken: string
}

export interface Account {
  id: string
  label: string
  createdAt: number
  preview: string
  expiresAt?: number
  hasRefresh: boolean
  lastRefreshedAt?: number
  machineCode: string
  proxyUrl?: string
}

export interface AccountHealth {
  id: string
  label: string
  inFlight: number
  available: boolean
  cooldownForMs: number
  quarantinedForMs: number
  consecutiveFailures: number
  lastOutcome?: string
  lastError?: string
}

export interface AccountCheck {
  id: string
  label: string
  ok: boolean
  detail: string
  plan?: string
  usedPercent?: number
  exhausted?: boolean
}

export interface ImportResult {
  added: number
  duplicates: number
  invalid: number
  exchanged: number
  details: { label: string; status: string }[]
}

export interface ProxyKey {
  id: string
  name: string
  prefix: string
  createdAt: number
  disabled: boolean
  lastUsedAt: number
}

export interface ModelInfo {
  id: string
  aliases: string[]
  displayName?: string
}

export interface LogEntry {
  id: number
  time: number
  kind: string
  model?: string
  account?: string
  keyPrefix?: string
  stream?: boolean
  status: string
  httpStatus?: number
  ms: number
  chars?: number
  tokens?: number
  error?: string
}

export interface LogStats {
  total: number
  ok: number
  error: number
  chatCount: number
  totalChars: number
  avgMs: number
  lastMinute: number
}

export interface LogsPayload {
  entries: LogEntry[]
  stats: LogStats
}

export type VpnMode = 'url-test' | 'fallback' | 'load-balance'

export interface VpnNode {
  name: string
  delay: number
}

export interface VpnStatus {
  installed: boolean
  running: boolean
  subUrl: string
  proxyUrl: string
  mode: VpnMode
  current?: string
  nodes: VpnNode[]
}

export interface UnknownTool {
  id: number
  time: number
  field: number
  name?: string
  model: string
  callId?: string
  hint?: string
  structure?: string
  rawBase64?: string
  count: number
}

export interface TestChatResult {
  ok: boolean
  text: string
  reasoning: string
  error?: string
}

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

let token = ''
let onUnauthorized: (() => void) | null = null

export function setToken(value: string) {
  token = value
}

export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Authorization', `Bearer ${token}`)
  if (init.body) headers.set('Content-Type', 'application/json')

  let res: Response
  try {
    res = await fetch(path, { ...init, headers })
  } catch {
    throw new ApiError(0, '无法连接到服务，请确认 Go 服务正在运行')
  }

  if (res.status === 401) {
    onUnauthorized?.()
    throw new ApiError(401, '管理口令无效或已失效')
  }

  const text = await res.text()
  let data: unknown = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }

  if (!res.ok) {
    let msg = `请求失败 (HTTP ${res.status})`
    if (data && typeof data === 'object' && 'error' in data) {
      const detail = String((data as { error: unknown }).error)
      if (detail) msg = detail
    }
    throw new ApiError(res.status, msg)
  }
  return data as T
}

const post = <T>(path: string, body?: unknown) =>
  request<T>(path, { method: 'POST', body: body === undefined ? undefined : JSON.stringify(body) })

const del = <T>(path: string) => request<T>(path, { method: 'DELETE' })

export const api = {
  serverInfo: () => request<ServerInfo>('/manage/server/info'),

  accounts: {
    list: () => request<Account[]>('/manage/accounts'),
    health: () => request<AccountHealth[]>('/manage/accounts/health'),
    add: (token: string, label: string) =>
      post<{ id: string; label: string; exchanged: boolean }>('/manage/accounts/add', { token, label }),
    import: (text: string) => post<ImportResult>('/manage/accounts/import', { text }),
    remove: (id: string) => del<{ deleted: boolean }>(`/manage/accounts/${id}`),
    check: (id: string) => request<AccountCheck>(`/manage/accounts/${id}/check`),
    checkAll: () => request<AccountCheck[]>('/manage/accounts/check-all'),
    setProxy: (id: string, url: string) => post<{ ok: boolean }>(`/manage/accounts/${id}/proxy`, { url }),
  },

  keys: {
    list: () => request<ProxyKey[]>('/manage/keys'),
    create: (name: string) =>
      post<{ id: string; name: string; key: string; createdAt: number }>('/manage/keys', { name }),
    revoke: (id: string) => del<{ revoked: boolean }>(`/manage/keys/${id}`),
  },

  models: {
    // 归一化 aliases：上游可能不返回该字段，避免调用方到处判空
    list: async () => {
      const list = await request<ModelInfo[]>('/manage/models')
      return (list ?? []).map((m) => ({ ...m, aliases: m.aliases ?? [] }))
    },
  },

  logs: {
    get: (since = 0) => request<LogsPayload>(`/manage/logs?since=${since}`),
    clear: () => post<{ ok: boolean }>('/manage/logs/clear'),
  },

  unknownTools: {
    list: async () => (await request<UnknownTool[]>('/manage/unknown-tools')) ?? [],
    clear: () => post<{ ok: boolean }>('/manage/unknown-tools/clear'),
  },

  vpn: {
    status: () => request<VpnStatus>('/manage/vpn/status'),
    setSub: (url: string) => post<{ ok: boolean }>('/manage/vpn/sub', { url }),
    setMode: (mode: VpnMode) => post<{ ok: boolean }>('/manage/vpn/mode', { mode }),
    enable: (url: string, mode: VpnMode) => post<{ ok: boolean }>('/manage/vpn/enable', { url, mode }),
    disable: () => post<{ ok: boolean }>('/manage/vpn/disable'),
    install: () => post<{ installed: boolean }>('/manage/vpn/install'),
    test: () => post<VpnStatus>('/manage/vpn/test'),
    switch: (name: string) => post<{ ok: boolean }>('/manage/vpn/switch', { name }),
  },

  chat: {
    test: (model: string, prompt: string, accountId?: string) =>
      post<TestChatResult>('/manage/chat/test', { model, prompt, accountId: accountId ?? '' }),
  },
}

/** 流式测试对话：逐段回调增量，返回一个可中止的句柄。 */
export function streamTestChat(
  body: { model: string; prompt: string; accountId?: string; images?: string[] },
  handlers: {
    onDelta: (d: { content?: string; reasoning?: string }) => void
    onError: (message: string) => void
    onDone: () => void
  },
) {
  const controller = new AbortController()

  void (async () => {
    try {
      const res = await fetch('/manage/chat/test-stream', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...body, accountId: body.accountId ?? '' }),
        signal: controller.signal,
      })
      if (res.status === 401) {
        onUnauthorized?.()
        handlers.onError('管理口令无效或已失效')
        return
      }
      if (!res.ok || !res.body) {
        handlers.onError(`请求失败 (HTTP ${res.status})`)
        return
      }

      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ''

      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })

        // SSE 以空行分隔事件块
        let sep: number
        while ((sep = buffer.indexOf('\n\n')) !== -1) {
          const chunk = buffer.slice(0, sep)
          buffer = buffer.slice(sep + 2)
          for (const line of chunk.split('\n')) {
            if (!line.startsWith('data:')) continue
            const payload = line.slice(5).trim()
            if (!payload || payload === '[DONE]') continue
            try {
              const d = JSON.parse(payload) as { content?: string; reasoning?: string; error?: string }
              if (d.error) handlers.onError(d.error)
              else handlers.onDelta(d)
            } catch {
              /* 忽略无法解析的片段 */
            }
          }
        }
      }
      handlers.onDone()
    } catch (err) {
      if ((err as Error).name === 'AbortError') {
        handlers.onDone()
        return
      }
      handlers.onError((err as Error).message)
    }
  })()

  return { abort: () => controller.abort() }
}
