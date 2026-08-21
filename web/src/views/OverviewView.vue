<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import EventTimeline from '@/components/console/EventTimeline.vue'
import KpiCard from '@/components/console/KpiCard.vue'
import NodeCard from '@/components/console/NodeCard.vue'
import { useEventsStore } from '@/stores/events'
import { useNodesStore } from '@/stores/nodes'
import { useOverviewStore } from '@/stores/overview'

type Filter = 'all' | 'ok' | 'warn' | 'down' | 'drift'

const route = useRoute()
const router = useRouter()
const nodes = useNodesStore()
const events = useEventsStore()
const overview = useOverviewStore()

const filter = computed<Filter>({
  get: () => (route.query.filter as Filter) ?? 'all',
  set: (v) => {
    void router.replace({ query: v === 'all' ? {} : { filter: v } })
  },
})

const selected = ref<string | null>(null)

const CHIPS: [Filter, string][] = [
  ['all', '全部'],
  ['ok', '在线'],
  ['warn', '异常'],
  ['down', '离线'],
  ['drift', '配置漂移'],
]

const shown = computed(() => {
  const f = filter.value
  if (f === 'all') return nodes.items
  if (f === 'drift') return nodes.items.filter((n) => n.drift)
  return nodes.items.filter((n) => n.status === f)
})

const kpis = computed(() => {
  const k = overview.kpi
  if (!k) return []
  // 三档全部取自后端同一条语句（契约 §3）。**不自己从节点列表推导** ——
  // 两边分别算迟早会对不上，而对不上的数字会在界面上冒出来两次。
  const trouble = [k.nodesWarn ? `异常 ${k.nodesWarn} 个` : '', k.nodesDown ? `离线 ${k.nodesDown} 个` : '']
    .filter(Boolean)
    .join(' · ')

  return [
    {
      key: 'all' as Filter,
      label: '节点在线',
      // 只有 status=ok 才算在线。把 warn 也算进来的话，脚注「异常 N 个 · 离线 M 个」
      // 与这个分子对不上账 —— 异常节点会同时被算成在线又被点名为异常。
      value: `${k.nodesOnline}/${k.nodesTotal}`,
      unit: '',
      foot: trouble || '全网正常',
      footColor: trouble ? 'var(--warning-text)' : 'var(--success-text)',
      caveat: '',
    },
    {
      key: 'all' as Filter,
      label: '全网连接数',
      value: (k.connsTotal / 1000).toFixed(1),
      unit: 'k',
      // null = 历史不足（冷启动第一天）。留白，不显示 0% —— 那会被读成「持平」。
      foot:
        k.connsDeltaPct === null
          ? '历史不足，暂无同比'
          : `较昨日同时段 ${k.connsDeltaPct >= 0 ? '+' : ''}${k.connsDeltaPct.toFixed(1)}%`,
      footColor:
        k.connsDeltaPct === null
          ? 'var(--text-faint)'
          : k.connsDeltaPct >= 0
            ? 'var(--success-text)'
            : 'var(--text-muted)',
      caveat: '',
    },
    {
      key: 'all' as Filter,
      label: '回源率',
      // null = 还没有流量样本。不要当成 0 —— 「0% 回源」是一个很强的说法，
      // 意味着边缘挡下了全部请求，而真相是我们还不知道。
      value: k.originRate === null ? '—' : k.originRate.toFixed(1),
      unit: k.originRate === null ? '' : '%',
      // 越低越好：没到达源站的那部分是被访问规则拦下的，**不是缓存命中**
      // —— 官方 Caddy 没有 HTTP 缓存模块（ADR-0001 / ADR-0003 的前提）
      foot:
        k.originRate === null
          ? '还没有流量样本'
          : `边缘拦截 ${(100 - k.originRate).toFixed(1)}% 请求`,
      footColor:
        k.originRate !== null && k.originRate > 30
          ? 'var(--warning-text)'
          : 'var(--text-muted)',
      caveat:
        '到达源站的请求占比。剩下的是被访问规则拦下（静默断连 / 403 / 404）或由静态响应处理掉的，不是缓存命中——边缘跑的官方 Caddy 没有缓存模块。',
    },
    {
      key: 'drift' as Filter,
      label: '配置漂移',
      value: String(k.driftNodes),
      unit: '个节点',
      // ADR-0002：这个 KPI 回答的是「这次下发到没到」，脚注就该直说
      foot: k.driftNodes
        ? `${k.driftNodes} 个节点未收到最近一次下发，点击筛选`
        : '最近一次下发全部到达',
      footColor: k.driftNodes ? 'var(--warning-text)' : 'var(--text-muted)',
      caveat:
        '只比对节点上报的版本号，不检查节点上的实际配置。有人 SSH 上去手改过的配置不会在这里显示。',
    },
  ]
})

const detail = computed(() => (selected.value ? nodes.byId.get(selected.value) : undefined))
</script>

<template>
  <!--
    KPI 取不到时不能让这一行凭空消失 —— 那会让人以为「本来就没有指标」。
    宁可留一条明确的失败提示，也不留一片什么都没有的空白。
  -->
  <div v-if="!overview.kpi" class="kpi-fallback" :class="{ error: overview.error }">
    <span v-if="overview.loading">正在加载指标…</span>
    <template v-else-if="overview.error">
      <span>指标加载失败：{{ overview.error }}</span>
      <button class="mini" type="button" @click="overview.fetch()">重试</button>
    </template>
    <span v-else>暂无指标</span>
  </div>

  <div v-else class="kpis">
    <KpiCard
      v-for="(k, i) in kpis"
      :key="i"
      :label="k.label"
      :value="k.value"
      :unit="k.unit"
      :foot="k.foot"
      :foot-color="k.footColor"
      :caveat="k.caveat || undefined"
      :active="k.key !== 'all' && filter === k.key"
      @select="filter = k.key"
    />
  </div>

  <div class="grid">
    <section class="panel">
      <header class="panel-head">
        <div class="panel-title">边缘节点</div>
        <div class="panel-sub">心跳间隔 3s · gRPC 长连接</div>
      </header>

      <div class="chips">
        <span class="chips-label">筛选</span>
        <button
          v-for="[key, label] in CHIPS"
          :key="key"
          class="chip"
          :class="{ on: filter === key }"
          type="button"
          @click="filter = key"
        >
          {{ label }}
        </button>
        <span class="chips-count">{{ shown.length }} / {{ nodes.items.length }}</span>
      </div>

      <div v-if="nodes.loading && !nodes.items.length" class="hint">正在加载节点…</div>
      <div v-else-if="nodes.error" class="hint error">
        {{ nodes.error }}
        <button class="mini" type="button" @click="nodes.fetchAll()">重试</button>
      </div>
      <div v-else-if="!nodes.items.length" class="hint">
        还没有边缘节点。到「边缘节点」页用一次性 Token 接入第一台。
      </div>
      <div v-else-if="!shown.length" class="hint">
        当前筛选下没有节点。
        <button class="mini" type="button" @click="filter = 'all'">显示全部</button>
      </div>

      <div v-else class="cards">
        <NodeCard
          v-for="n in shown"
          :key="n.id"
          :node="n"
          :selected="selected === n.id"
          @select="selected = selected === n.id ? null : n.id"
        />
      </div>
    </section>

    <aside class="panel">
      <template v-if="detail">
        <header class="panel-head">
          <div class="panel-title mono">{{ detail.id }}</div>
          <button class="mini" type="button" @click="selected = null">返回事件</button>
        </header>
        <div class="detail">
          <dl>
            <dt>位置</dt>
            <dd>{{ detail.city }} · {{ detail.vendor }}</dd>
            <dt>线路</dt>
            <dd class="mono">{{ detail.line }}</dd>
            <dt>公网 IP</dt>
            <dd class="mono">{{ detail.ip }}</dd>
            <dt>配置版本</dt>
            <dd class="mono">
              {{ detail.cfgVersion }}
              <span v-if="detail.drift" class="warn">· 未收到最近一次下发</span>
            </dd>
            <dt>生效路由 / 规则</dt>
            <dd class="mono">{{ detail.routes }} / {{ detail.rules }}</dd>
            <dt>DNS 解析</dt>
            <dd :class="{ warn: !detail.dnsEnabled }">
              {{ detail.dnsEnabled ? '正常参与解析' : '已暂停' }}
            </dd>
          </dl>
        </div>
      </template>

      <template v-else>
        <header class="panel-head">
          <div class="panel-title">最近事件</div>
          <div class="panel-sub">{{ events.items.length }} 条</div>
        </header>
        <EventTimeline :events="events.items" />
      </template>
    </aside>
  </div>
</template>

<style scoped>
.kpi-fallback {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--space-2);
  padding: var(--space-5) var(--space-4);
  background: var(--surface-card);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-xs);
  font-size: var(--fs-sm);
  color: var(--text-muted);
}
.kpi-fallback.error {
  border-color: var(--danger);
  color: var(--danger-text);
}
.kpis {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 14px;
}
.grid {
  display: grid;
  grid-template-columns: 1fr 322px;
  gap: 18px;
  align-items: start;
}
.panel {
  background: var(--surface-card);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-xs);
  overflow: hidden;
}
.panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-3) var(--space-4);
  border-bottom: 1px solid var(--border-subtle);
}
.panel-title {
  font-size: var(--fs-sm);
  font-weight: var(--weight-bold);
  color: var(--text-strong);
}
.panel-sub {
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  color: var(--text-muted);
}
.chips {
  display: flex;
  align-items: center;
  gap: 7px;
  flex-wrap: wrap;
  padding: 10px var(--space-4);
  border-bottom: 1px solid var(--border-subtle);
}
.chips-label {
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  font-weight: var(--weight-semibold);
  color: var(--text-muted);
  flex: none;
}
.chip {
  padding: 2px 11px;
  border-radius: var(--radius-full);
  border: 1px solid var(--border-default);
  background: var(--surface-card);
  color: var(--text-muted);
  cursor: pointer;
  font-size: var(--fs-2xs);
  font-family: var(--font-mono);
  transition: var(--transition-colors);
}
.chip:hover {
  border-color: var(--border-strong);
}
.chip.on {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--text-on-accent);
  font-weight: var(--weight-semibold);
}
.chips-count {
  margin-left: auto;
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  color: var(--text-faint);
}
.cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(232px, 1fr));
  gap: var(--space-3);
  padding: 14px var(--space-4) var(--space-4);
}
.hint {
  padding: 42px var(--space-4);
  text-align: center;
  color: var(--text-muted);
  font-size: var(--fs-sm);
}
.hint.error {
  color: var(--danger-text);
}
.mini {
  padding: 3px 10px;
  border: 1px solid var(--border-default);
  background: var(--surface-card);
  color: var(--text-strong);
  border-radius: var(--radius-sm);
  cursor: pointer;
  font-size: var(--fs-xs);
  margin-left: var(--space-1-5);
}
.mini:hover {
  background: var(--surface-sunken);
}
.detail {
  padding: 14px var(--space-4);
}
.detail dl {
  margin: 0;
  display: grid;
  grid-template-columns: auto 1fr;
  gap: var(--space-2) var(--space-3);
  font-size: var(--fs-xs);
}
.detail dt {
  color: var(--text-muted);
  white-space: nowrap;
}
.detail dd {
  margin: 0;
  color: var(--text-strong);
}
.mono {
  font-family: var(--font-mono);
}
.warn {
  color: var(--warning-text);
}
</style>
