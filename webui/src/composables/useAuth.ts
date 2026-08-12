import { computed, ref } from 'vue'
import { api, setToken, setUnauthorizedHandler, type ServerInfo } from '@/lib/api'

const STORAGE_KEY = 'cursor-proxy.admin-token'

const token = ref(localStorage.getItem(STORAGE_KEY) ?? '')
const info = ref<ServerInfo | null>(null)
const ready = ref(false)

setToken(token.value)
setUnauthorizedHandler(() => {
  token.value = ''
  info.value = null
  localStorage.removeItem(STORAGE_KEY)
  setToken('')
})

/** 管理口令状态。口令存在 localStorage，刷新后自动恢复会话。 */
export function useAuth() {
  const authed = computed(() => !!token.value && !!info.value)

  /** 用给定口令验证并建立会话。 */
  async function login(value: string) {
    const trimmed = value.trim()
    if (!trimmed) throw new Error('请填写管理口令 ADMIN_TOKEN')
    setToken(trimmed)
    const serverInfo = await api.serverInfo()
    token.value = trimmed
    info.value = serverInfo
    localStorage.setItem(STORAGE_KEY, trimmed)
  }

  /** 启动时用已存口令静默恢复会话。 */
  async function restore() {
    if (!token.value) {
      ready.value = true
      return
    }
    try {
      info.value = await api.serverInfo()
    } catch {
      token.value = ''
      localStorage.removeItem(STORAGE_KEY)
      setToken('')
    } finally {
      ready.value = true
    }
  }

  function logout() {
    token.value = ''
    info.value = null
    localStorage.removeItem(STORAGE_KEY)
    setToken('')
  }

  return { token, info, authed, ready, login, restore, logout }
}
