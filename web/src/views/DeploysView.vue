<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import DeployProgress from '@/components/workbench/DeployProgress.vue'
import { http } from '@/api/http'
import type { DeployDetailWire, DeployWire, Paged, RollbackWire } from '@/api/types'
import { useConfigStore } from '@/stores/config'
import { useNodesStore } from '@/stores/nodes'
import { useUiStore } from '@/stores/ui'
import { fmtClock } from '@/utils/format'

/**
 * 下发记录 —— 排障链条上「追溯」的那一半。
 *
 * 回答两个问题：这次下发**哪个节点失败了、为什么**；以及**回到上一版**。
 * 回滚不直接下发（契约 §7.5）：它把差异写回草稿，人在工作台确认 diff 后
 * 走同一条流水线 —— 回滚和正常改配置经过完全相同的校验与审计。
 */
const router = useRouter()
const nodes = useNodesStore()
const config = useConfigStore()
const ui = useUiStore()

const items = ref<DeployWire[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const open = ref<string | null>(null)
const details = ref<Record<number, DeployDetailWire>>({})
const rollingBack = ref<string | null>(null)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    items.value = (await http.get<Paged<DeployWire>>('/deploys')).items
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载下发记录失败'
  } finally {
    loading.value = false
  }
}

onMounted(load)

async function expand(d: DeployWire): Promise<void> {
  if (open.value === d.cfg_version) {
    open.value = null
    return
  }
  open.value = d.cfg_version
  if (!details.value[d.id]) {
    try {
      details.value = { ...details.value, [d.id]: await http.get<DeployDetailWire>(`/deploys/${d.id}`) }
    } catch (e) {
      ui.toast('warn', '加载详情失败', e instanceof Error ? e.message : '')
    }
  }
}

async function rollback(d: DeployWire): Promise<void> {
  rollingBack.value = d.cfg_version
  try {
    const r = await http.post<RollbackWire>(
      `/deploys/${encodeURIComponent(d.cfg_version)}/rollback`,
    )
    await config.fetchAll().catch(() => {})
    ui.toast(
      'info',
      `已把 ${d.cfg_version} 的差异写回草稿`,
      `${r.res_keys.length} 个资源 · 回滚不直接下发，请在工作台确认后再下发`,
    )
    // 直接送到第一个被写回的资源上，省一次找
    const first = r.res_keys[0]
    await router.push({ name: 'workbench', params: first ? { key: first } : {} })
  } catch (e) {
    ui.toast('warn', '回滚失败', e instanceof Error ? e.message : '')
  } finally {
    rollingBack.value = null
  }
}

const nodeCity = (id: string) => nodes.byId.get(id)?.city ?? ''

const resultTone = (d: DeployWire) =>
  d.fail_count === 0 ? 'ok' : d.ok_count === 0 ? 'danger' : 'warn'
const resultText = (d: DeployWire) =>
  d.fail_count === 0 ? `${d.ok_count} 个节点全部接受` : `${d.ok_count} 成功 / ${d.fail_count} 失败`

const detailOf = computed(() => (id: number) => details.value[id])
</script>

<template>
  <section class="panel">
    <header class="head">
      <div class="title">下发记录</div>
      <div class="sub">每次下发的快照 · 回滚会把差异写回草稿，不直接下发</div>
    </header>

    <div v-if="loading && !items.length" class="hint">正在加载…</div>
    <div v-else-if="error" class="hint error">
      {{ error }}
      <button class="mini" type="button" @click="load">重试</button>
    </div>
    <div v-else-if="!items.length" class="hint">还没有下发记录。</div>

    <table v-else class="table">
      <thead>
        <tr>
          <th>版本</th>
          <th>时间</th>
          <th>操作人</th>
          <th>变更资源</th>
          <th>结果</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <template v-for="d in items" :key="d.cfg_version">
          <tr :class="{ on: open === d.cfg_version }">
            <td>
              <span class="mono strong">{{ d.cfg_version }}</span>
              <span v-if="d.is_baseline" class="tag ok base">当前基线</span>
            </td>
            <td class="mono muted">{{ fmtClock(d.created_at) }}</td>
            <td class="mono muted">{{ d.operator }}</td>
            <td class="mono muted">{{ d.res_keys.join('、') || '—' }}</td>
            <td>
              <span class="tag" :class="resultTone(d)">{{ resultText(d) }}</span>
            </td>
            <td class="right">
              <button class="mini" type="button" @click="expand(d)">
                {{ open === d.cfg_version ? '收起' : '逐节点结果' }}
              </button>
              <!--
                当前基线不可回滚（契约 §7.3）—— 回到自己是空操作，
                给一个能点但什么也不做的按钮只会让人怀疑系统坏了。
              -->
              <button
                class="mini"
                type="button"
                :disabled="d.is_baseline || rollingBack === d.cfg_version"
                :title="d.is_baseline ? '这是当前基线，无需回滚' : '把该版本与基线的差异写回草稿'"
                @click="rollback(d)"
              >
                {{ rollingBack === d.cfg_version ? '写回中…' : '回滚' }}
              </button>
            </td>
          </tr>
          <tr v-if="open === d.cfg_version" class="detail-row">
            <td colspan="6">
              <div v-if="detailOf(d.id)" class="detail">
                <DeployProgress :rows="detailOf(d.id)!.results" :node-label="nodeCity" />
                <p class="note">
                  「已接受」指该节点的 Caddy 收下了这份配置，不代表流量已经在走。
                </p>
              </div>
              <div v-else class="detail muted">正在加载逐节点结果…</div>
            </td>
          </tr>
        </template>
      </tbody>
    </table>
  </section>
</template>

<style scoped>
@import './catalog.css';
.tag.base {
  margin-left: var(--space-2);
}
.table tbody tr.on {
  background: var(--surface-sunken);
}
.detail-row td {
  background: var(--surface-sunken);
}
.detail {
  padding: var(--space-2) 0;
}
.note {
  margin: var(--space-3) 0 0;
  font-size: var(--fs-micro);
  color: var(--text-faint);
}
.right .mini + .mini {
  margin-left: var(--space-1-5);
}
</style>
