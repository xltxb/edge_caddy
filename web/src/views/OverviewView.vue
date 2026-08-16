<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import StatusPill from '@/components/base/StatusPill.vue'
import { useNodesStore } from '@/stores/nodes'
import { useEventsStore } from '@/stores/events'

const nodes = useNodesStore()
const events = useEventsStore()
const route = useRoute()
const router = useRouter()

// 筛选状态同步到 query，刷新后仍在（前端文档 §3）
const filter = ref(String(route.query.filter ?? 'all'))
function setFilter(k: string) {
  filter.value = filter.value === k ? 'all' : k
  void router.replace({ query: filter.value === 'all' ? {} : { filter: filter.value } })
}

onMounted(() => nodes.load())
const shown = computed(() => nodes.filtered(filter.value))

const kpis = computed(() => [
  { key: 'online', label: '在线节点', value: `${nodes.onlineCount} / ${nodes.nodes.length}`, tone: '' },
  { key: 'down', label: '离线节点', value: String(nodes.nodes.length - nodes.onlineCount), tone: nodes.nodes.length - nodes.onlineCount ? 'bad' : '' },
  { key: 'drifted', label: '配置漂移', value: String(nodes.driftedCount), tone: nodes.driftedCount ? 'warn' : '' },
  { key: 'baseline', label: '当前基线', value: nodes.baseline || '尚未发布', tone: '', small: true },
])

function toneOf(s: string): 'ok' | 'warn' | 'danger' {
  return s === 'ok' ? 'ok' : s === 'warn' ? 'warn' : 'danger'
}
function kindTone(k: string): 'ok' | 'warn' | 'danger' {
  return k === 'crit' ? 'danger' : k === 'warn' ? 'warn' : 'ok'
}
</script>

<template>
  <div class="wrap">
    <section class="card kpis">
      <button v-for="k in kpis" :key="k.key" class="kpi"
              :class="{ on: filter === k.key, clickable: k.key !== 'baseline' }"
              :disabled="k.key === 'baseline'"
              @click="k.key !== 'baseline' && setFilter(k.key)">
        <div class="k">{{ k.label }}</div>
        <div class="v mono" :class="[k.tone, { sm: k.small }]">{{ k.value }}</div>
      </button>
    </section>
    <p v-if="filter !== 'all'" class="filterbar">
      正在按「{{ kpis.find((k) => k.key === filter)?.label }}」筛选 ·
      <button class="lnk" @click="setFilter(filter)">清除</button>
    </p>

    <div class="cols">
      <section class="card">
        <div class="ch">节点</div>
        <div v-if="!shown.length" class="pad empty">
          {{ nodes.nodes.length ? '没有符合筛选条件的节点。' : '还没有节点接入。' }}
        </div>
        <div v-for="n in shown" :key="n.id" class="nrow">
          <span class="mono id">{{ n.id }}</span>
          <span class="sub">{{ n.city || '—' }}</span>
          <span class="mono sub">CPU {{ (n.cpu ?? 0).toFixed(1) }}%</span>
          <span>
            <StatusPill v-if="n.drifted" tone="warn" text="待同步" />
            <StatusPill :tone="toneOf(n.status)" :text="n.status === 'ok' ? '在线' : n.status === 'warn' ? '异常' : '离线'" />
          </span>
        </div>
      </section>

      <section class="card">
        <div class="ch">事件流</div>
        <div v-if="!events.events.length" class="pad empty">暂无事件。节点掉线或恢复时会出现在这里。</div>
        <div v-for="(e, i) in events.events" :key="i" class="erow">
          <span class="mono t">{{ e.t }}</span>
          <StatusPill :tone="kindTone(e.kind)" :text="e.node" />
          <span class="msg">{{ e.msg }}</span>
        </div>
      </section>
    </div>

    <p class="note">
      「配置漂移」只比对节点上报的版本号与当前基线，<b>不检查节点上的配置内容</b>——
      有人 SSH 上去手改配置时它不会亮。
    </p>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 12px; }
.card { background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; }
.kpis { display: grid; grid-template-columns: repeat(4, 1fr); }
.kpi { padding: 16px 18px; border: 0; border-right: 1px solid var(--border-subtle); background: none; text-align: left; }
.kpi:last-child { border-right: 0; }
.kpi.clickable { cursor: pointer; }
.kpi.clickable:hover { background: var(--surface-sunken); }
.kpi.on { background: var(--accent-subtle); }
.k { font-size: 10.5px; letter-spacing: .08em; text-transform: uppercase; color: var(--text-faint); font-weight: 600; }
.v { font-size: 25px; font-weight: 700; color: var(--text-strong); margin-top: 5px; font-variant-numeric: tabular-nums; }
.v.sm { font-size: 14px; }
.v.warn { color: var(--warning-text); }
.v.bad { color: var(--danger-text); }
.filterbar { margin: 0; font-size: 12px; color: var(--text-muted); }
.lnk { background: none; border: 0; cursor: pointer; font-size: 12px; color: var(--accent-text); padding: 0; }
.cols { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.ch { padding: 11px 14px; border-bottom: 1px solid var(--border-subtle); font-size: 12.5px; font-weight: 700; color: var(--text-strong); }
.pad { padding: 14px; }
.empty { color: var(--text-muted); font-size: 12.5px; }
.nrow { display: grid; grid-template-columns: minmax(0,1fr) 70px 110px auto; gap: 10px; align-items: center; padding: 9px 14px; border-bottom: 1px solid var(--border-subtle); }
.nrow:last-child { border-bottom: 0; }
.id { font-size: 12.5px; font-weight: 600; color: var(--text-strong); }
.sub { font-size: 11.5px; color: var(--text-muted); }
.erow { display: flex; align-items: center; gap: 9px; padding: 8px 14px; border-bottom: 1px solid var(--border-subtle); }
.erow:last-child { border-bottom: 0; }
.t { font-size: 11px; color: var(--text-faint); }
.msg { font-size: 12px; color: var(--text-body); }
.note { margin: 0; font-size: 11.5px; color: var(--text-faint); line-height: 1.7; }
</style>
