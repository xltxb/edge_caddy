<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import DeployProgress from '@/components/workbench/DeployProgress.vue'
import { http } from '@/api/http'
import type {
  DeployDetailWire,
  DeployWire,
  Paged,
  RollbackSkipped,
  RollbackWire,
} from '@/api/types'
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
/** 回滚覆盖不到的资源。有值时**不自动跳转** —— 跳走会把这条警告一起扫掉。 */
const skipped = ref<{ cfg: string; items: RollbackSkipped[]; done: string[] } | null>(null)

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

    /*
     * 有覆盖不到的资源时**不自动跳转**。
     *
     * 自动跳工作台是个便利，但它会把这条警告一起扫掉 —— toast 4.2 秒就没了，
     * 而「某条路由其实没回去」这件事要等到下次出问题才会被发现。
     * 停下来把它列清楚，让人自己决定下一步。
     */
    if (r.skipped.length > 0) {
      skipped.value = { cfg: d.cfg_version, items: r.skipped, done: r.res_keys }
      return
    }

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

async function goWorkbench(): Promise<void> {
  const first = skipped.value?.done[0]
  skipped.value = null
  await router.push({ name: 'workbench', params: first ? { key: first } : {} })
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

    <div v-if="skipped" class="skipped">
      <div class="sk-title">
        回滚到 {{ skipped.cfg }} —— {{ skipped.done.length }} 个资源已写回草稿，
        <b>{{ skipped.items.length }} 个覆盖不到</b>
      </div>
      <ul class="sk-list">
        <li v-for="s in skipped.items" :key="s.res_key">
          <code>{{ s.res_key }}</code> —— {{ s.reason }}
        </li>
      </ul>
      <p class="sk-note">
        覆盖不到的这些不会自动处理。如果确实要让它们回到那一版，需要在工作台手工改。
      </p>
      <div class="sk-actions">
        <button class="mini" type="button" @click="skipped = null">留在本页</button>
        <button class="primary" type="button" @click="goWorkbench">去工作台确认已写回的</button>
      </div>
    </div>

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
.skipped {
  padding: var(--space-3) var(--space-4);
  background: var(--warning-subtle);
  border-bottom: 1px solid var(--border-subtle);
}
.sk-title {
  font-size: var(--fs-xs);
  color: var(--warning-text);
  margin-bottom: var(--space-2);
}
.sk-list {
  margin: 0 0 var(--space-2);
  padding-left: 18px;
  font-size: var(--fs-2xs);
  color: var(--text-body);
  line-height: 1.8;
}
.sk-list code {
  font-family: var(--font-mono);
  color: var(--text-strong);
}
.sk-note {
  margin: 0 0 var(--space-3);
  font-size: var(--fs-micro);
  color: var(--text-muted);
}
.sk-actions {
  display: flex;
  gap: var(--space-2);
}
.sk-actions .primary {
  padding: 4px 12px;
  border: 1px solid var(--accent);
  border-radius: var(--radius-sm);
  background: var(--accent);
  color: var(--text-on-accent);
  font-size: var(--fs-micro);
  font-weight: var(--weight-semibold);
  cursor: pointer;
}
</style>
