<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import EventTimeline from '@/components/console/EventTimeline.vue'
import KpiCard from '@/components/console/KpiCard.vue'
import NodeCard from '@/components/console/NodeCard.vue'
import { useEventsStore } from '@/stores/events'
import { useNodesStore } from '@/stores/nodes'
import { useOverviewStore } from '@/stores/overview'
import { buildKpis } from '@/overview/kpis'

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

/**
 * 四格 KPI 的构建在 `@/overview/kpis` 里，不在这个文件里。
 *
 * 抽出去不是为了复用（只有这一处用），是为了**能被证伪**：这一屏是人打开控制台
 * 看到的第一样东西，四格每一格都是一句关于全网的断言，而写在 computed 里的断言
 * 只在有人盯着截图看的时候被检查过一次。
 */
const kpis = computed(() => (overview.kpi ? buildKpis(overview.kpi) : []))

const TONE: Record<string, string> = {
  ok: 'var(--success-text)',
  warn: 'var(--warning-text)',
  muted: 'var(--text-muted)',
  faint: 'var(--text-faint)',
}

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
      :foot-color="TONE[k.tone]"
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
