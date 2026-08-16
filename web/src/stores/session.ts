import { defineStore } from 'pinia'
import { ref } from 'vue'
import { get, post } from '@/api/http'

export interface Me {
  user: string
  /** 主控是否启用了鉴权。为 false 时接口敞开，不该把人挡在登录页外面。 */
  auth_required: boolean
}

type Fetcher = () => Promise<Me>

export const useSessionStore = defineStore('session', () => {
  const user = ref('')
  const authRequired = ref(true)
  const ready = ref(false)

  let fetcher: Fetcher = () => get<Me>('/me')

  /** __setFetcher 只供测试替换网络边界。 */
  function __setFetcher(f: Fetcher) {
    fetcher = f
    ready.value = false
  }

  async function refresh() {
    try {
      const me = await fetcher()
      user.value = me.user
      authRequired.value = me.auth_required
    } catch {
      // 401 与网络错误都归为「当前没有可用会话」。区分它们对守卫没有意义：
      // 两种情况下都不能放行。
      user.value = ''
      authRequired.value = true
    } finally {
      ready.value = true
    }
  }

  /** needsLogin 为 true 时守卫会把访问者送去登录页。 */
  function needsLogin() {
    return authRequired.value && user.value === ''
  }

  async function login(name: string, password: string) {
    const data = await post<{ user: string }>('/login', { user: name, password })
    user.value = data.user
    authRequired.value = true
    ready.value = true
  }

  async function logout() {
    try {
      await post('/logout')
    } finally {
      user.value = ''
      ready.value = true
    }
  }

  return { user, authRequired, ready, refresh, needsLogin, login, logout, __setFetcher }
})
