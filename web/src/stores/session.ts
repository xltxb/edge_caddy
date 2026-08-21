import { defineStore } from 'pinia'
import { ref } from 'vue'
import { http } from '@/api/http'
import type { SessionWire } from '@/api/types'

export const useSessionStore = defineStore('session', () => {
  const operator = ref<string | null>(null)
  /** 尚未问过后端「我登录了吗」。路由守卫要等它落定再判断。 */
  const resolved = ref(false)

  /**
   * 探测当前会话。
   *
   * `GET /auth/session` 的 401 是「还没登录」这个**正常结果**，不是会话过期，
   * 所以走 http 的旁路方法，不触发全局跳转——否则登录页自己会把自己踢一遍。
   */
  async function probe(): Promise<void> {
    try {
      const s = await http.getBypassingAuth<SessionWire>('/auth/session')
      operator.value = s?.username ?? null
    } catch {
      operator.value = null
    } finally {
      resolved.value = true
    }
  }

  async function login(username: string, password: string): Promise<void> {
    const s = await http.post<SessionWire>('/auth/login', { username, password })
    operator.value = s.username
    resolved.value = true
  }

  async function logout(): Promise<void> {
    try {
      await http.post('/auth/logout')
    } finally {
      operator.value = null
    }
  }

  return { operator, resolved, probe, login, logout }
})
