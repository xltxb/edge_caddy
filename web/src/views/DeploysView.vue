<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import StatusPill from '@/components/base/StatusPill.vue'
import { get, post } from '@/api/http'
import type { DeployResultRow } from '@/api/types'

interface DeployRow {
  id: number
  cfg_version: string
  operator: string
  res_keys: string[]
  ok_count: number
  fail_count: number
  created_at: string
  results: DeployResultRow[]
  is_baseline: boolean
}

const router = useRouter()
const deploys = ref<DeployRow[]>([])
const open = ref<Record<number, boolean>>({})
const err = ref('')
const busy = ref('')

async function load() {
  err.value = ''
  try {
    deploys.value = (await get<{ deploys: DeployRow[] }>('/deploys')).deploys ?? []
  } catch (e) {
    err.value = (e as Error).message
  }
}
onMounted(load)

async function rollback(d: DeployRow) {
  if (!window.confirm(
    `回滚到 ${d.cfg_version}？\n\n` +
    '这一步只把该版本写回草稿，**不会直接下发**。\n' +
    '你需要在工作台检查 diff 后再推送。',
  )) return
  busy.value = d.cfg_version
  err.value = ''
  try {
    await post(`/deploys/${encodeURIComponent(d.cfg_version)}/rollback`)
    await router.push('/workbench')
  } catch (e) {
    err.value = (e as Error).message
  } finally {
    busy.value = ''
  }
}

function fmt(t: string) {
  return t ? t.replace('T', ' ').slice(0, 19) : '—'
}
</script>

<template>
  <div class="wrap">
    <section v-if="err" class="card pad e">{{ err }}</section>
    <section class="card">
      <div class="thead mono">
        <span>版本</span><span>操作人</span><span>资源</span><span>结果</span><span>时间</span><span></span>
      </div>
      <div v-if="!deploys.length" class="pad empty">还没有下发记录。</div>

      <template v-for="d in deploys" :key="d.id">
        <div class="row">
          <span class="mono v">
            {{ d.cfg_version }}
            <StatusPill v-if="d.is_baseline" tone="ok" text="当前基线" />
          </span>
          <span class="mono sub">{{ d.operator }}</span>
          <span class="mono sub">{{ d.res_keys.length }} 项</span>
          <span class="sub">
            <StatusPill :tone="d.fail_count ? 'danger' : 'ok'"
                        :text="`${d.ok_count} 成功 / ${d.fail_count} 失败`" />
          </span>
          <span class="mono sub">{{ fmt(d.created_at) }}</span>
          <span class="ops">
            <button class="lnk" @click="open[d.id] = !open[d.id]">
              {{ open[d.id] ? '收起' : '明细' }}
            </button>
            <!-- 不提供「回滚到自己」：那是个空操作 -->
            <button v-if="!d.is_baseline" class="lnk" :disabled="busy === d.cfg_version"
                    @click="rollback(d)">
              {{ busy === d.cfg_version ? '写回中…' : '回滚' }}
            </button>
          </span>
        </div>
        <div v-if="open[d.id]" class="detail">
          <div v-for="r in d.results" :key="r.node" class="drow">
            <span class="mono">{{ r.node }}</span>
            <StatusPill :tone="r.state === 'ok' ? 'ok' : 'danger'"
                        :text="r.state === 'ok' ? '已生效' : '失败'" />
            <!-- 失败原文原样展示，不做归类：排查时唯一有用的就是它 -->
            <span class="mono res">{{ r.res }}</span>
          </div>
          <div v-if="!d.results.length" class="empty sm">没有逐节点结果。</div>
        </div>
      </template>
    </section>
    <p class="note">回滚只把该版本写回草稿，不直接下发——你要在工作台检查 diff 后再推送（PRD §6.3）。</p>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 12px; }
.card { background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; }
.pad { padding: 14px 16px; }
.e { color: var(--danger-text); font-size: 13px; }
.empty { color: var(--text-muted); font-size: 13px; }
.empty.sm { padding: 6px 0; font-size: 12px; }
.thead, .row { display: grid; grid-template-columns: minmax(0,1.2fr) 90px 70px 150px 150px 110px; gap: 12px; padding: 9px 16px; border-bottom: 1px solid var(--border-subtle); align-items: center; }
.thead { font-size: 10px; letter-spacing: .08em; text-transform: uppercase; color: var(--text-faint); font-weight: 600; }
.v { font-size: 12.5px; font-weight: 600; color: var(--text-strong); display: flex; align-items: center; gap: 7px; }
.sub { font-size: 11.5px; color: var(--text-muted); }
.ops { display: flex; gap: 10px; justify-content: flex-end; }
.lnk { background: none; border: 0; cursor: pointer; font-size: 12px; color: var(--accent-text); padding: 0; }
.detail { padding: 8px 16px 12px 16px; background: var(--surface-sunken); border-bottom: 1px solid var(--border-subtle); }
.drow { display: flex; align-items: center; gap: 10px; padding: 4px 0; font-size: 12px; }
.res { color: var(--text-muted); font-size: 11.5px; }
.note { margin: 0; font-size: 11.5px; color: var(--text-faint); }
</style>
