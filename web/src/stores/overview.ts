import { defineStore } from 'pinia'
import { ref } from 'vue'
import { http } from '@/api/http'
import type { OverviewWire } from '@/api/types'
import { fromKpiWire, type OverviewKpi } from '@/model'
import { useEventsStore } from './events'

export const useOverviewStore = defineStore('overview', () => {
  const kpi = ref<OverviewKpi | null>(null)
  const baseline = ref<string>('')
  const loading = ref(false)
  const error = ref<string | null>(null)

  async function fetch(): Promise<void> {
    loading.value = true
    error.value = null
    try {
      const data = await http.get<OverviewWire>('/overview')
      baseline.value = data.baseline
      kpi.value = fromKpiWire(data.kpi)
      useEventsStore().seed(data.events)
    } catch (e) {
      error.value = e instanceof Error ? e.message : '加载总览失败'
      throw e
    } finally {
      loading.value = false
    }
  }

  return { kpi, baseline, loading, error, fetch }
})
