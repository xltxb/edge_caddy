import { defineStore } from 'pinia'
import { ref } from 'vue'
import { get, post, del, put } from '@/api/http'
import type { DeployResponse, Route } from '@/api/types'
import { useDeployStore } from './deploys'

export const useRoutesStore = defineStore('routes', () => {
  const routes = ref<Route[]>([])
  const loading = ref(false)
  const loadError = ref('')

  /** 最近一次下发的逐节点结果。不允许「整体成功/失败」的黑盒（PRD §7）。 */
  const lastDeploy = ref<DeployResponse | null>(null)
  const deploying = ref(false)
  const deployError = ref('')

  async function load() {
    loading.value = true
    loadError.value = ''
    try {
      const r = await get<{ routes: Route[] }>('/routes')
      routes.value = r.routes ?? []
    } catch (e) {
      loadError.value = (e as Error).message
    } finally {
      loading.value = false
    }
  }

  async function create(body: Partial<Route>) {
    await post('/routes', body)
    await load()
  }

  async function update(domain: string, body: Partial<Route>) {
    await put(`/routes/${encodeURIComponent(domain)}`, body)
    await load()
  }

  async function remove(domain: string) {
    await del(`/routes/${encodeURIComponent(domain)}`)
    await load()
  }

  async function deploy() {
    deploying.value = true
    deployError.value = ''
    lastDeploy.value = null
    try {
      const r = await post<DeployResponse>('/deploys')
      lastDeploy.value = r
      // 接口返回时下发其实已经落定（编排是同步等结果的），把结果登记进
      // 进度 store 仍有意义：WS 帧在过程中已经推过一遍，这里补齐终态，
      // 两条路径的结论必须一致。
      useDeployStore().start(r.deploy_id, r.results.map((x) => x.node))
      for (const row of r.results) {
        useDeployStore().applyFrame({
          type: 'deploy_progress',
          data: { deploy_id: r.deploy_id, node: row.node, state: row.state, res: row.res },
        })
      }
      await load() // 版本号会随下发推进
    } catch (e) {
      deployError.value = (e as Error).message
    } finally {
      deploying.value = false
    }
  }

  return { routes, loading, loadError, lastDeploy, deploying, deployError, load, create, update, remove, deploy }
})
