<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { http } from '@/api/http'
import type { DnsWeightsWire } from '@/api/types'
import { isDivergent, lineInputs, mergedWeight, type LineInput } from '@/dns/capability'
import { useUiStore } from '@/stores/ui'

/**
 * DNS 调度。
 *
 * 两条容易被做错的地方：
 *
 * 1. **weight 与 share 不是一回事**（契约 §8）。weight 是你配的值，share 是
 *    实际占比。退出解析的节点 share 为 0，权重在该线路内的其余节点间重新
 *    归一化。混成一个数字就说不清「我配了 40，为什么它没在扛流量」。
 *
 * 2. **服务商的能力不对等**。Cloudflare 的 DNS 记录没有线路概念，电信 /
 *    联通 / 移动表达不了。所以界面按服务商能力**合并输入框**，而不是分别
 *    列出再在保存时拒绝 —— 后者是在人已经配完之后才告诉他做不到。
 */
const ui = useUiStore()

const data = ref<DnsWeightsWire | null>(null)
const loading = ref(false)
const saving = ref(false)
const error = ref<string | null>(null)
/** 本地编辑：`${lineCode}/${node}` → weight。合并组会一次写多条线路。 */
const edits = ref<Record<string, number>>({})

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    data.value = await http.get<DnsWeightsWire>('/dns/weights')
    edits.value = {}
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载解析权重失败'
  } finally {
    loading.value = false
  }
}

onMounted(load)

const caps = computed(() => data.value?.capabilities)
const configured = computed(() => !!caps.value?.kind)

/** 契约线路码 → 该线路的原始 entries，供合并组取值与算占比。 */
const byCode = computed(() => {
  const m: Record<string, DnsWeightsWire['lines'][number]> = {}
  for (const l of data.value?.lines ?? []) m[l.code] = l
  return m
})

/** 当前权重（含本地编辑），按线路码索引。 */
const weights = computed<Record<string, Record<string, number>>>(() => {
  const out: Record<string, Record<string, number>> = {}
  for (const l of data.value?.lines ?? []) {
    out[l.code] = {}
    for (const e of l.entries) out[l.code]![e.node] = edits.value[`${l.code}/${e.node}`] ?? e.weight
  }
  return out
})

const groups = computed<LineInput[]>(() =>
  lineInputs(
    (data.value?.lines ?? []).map((l) => ({ code: l.code, name: l.name })),
    caps.value?.lines,
  ),
)

/** 合并组的节点集合 = 各被覆盖线路节点的并集。 */
function nodesOf(g: LineInput): string[] {
  const s = new Set<string>()
  for (const c of g.covers) for (const e of byCode.value[c]?.entries ?? []) s.add(e.node)
  return [...s]
}

function entryOf(g: LineInput, node: string) {
  for (const c of g.covers) {
    const e = byCode.value[c]?.entries.find((x) => x.node === node)
    if (e) return e
  }
  return undefined
}

function weightOf(g: LineInput, node: string): number {
  return mergedWeight(weights.value, g, node)
}

/** 改一个合并组的权重 = 同时写它覆盖的每一条线路。 */
function setWeight(g: LineInput, node: string, raw: string): void {
  const v = Math.max(0, Math.round(Number(raw) || 0))
  const next = { ...edits.value }
  for (const c of g.covers) {
    const base = byCode.value[c]?.entries.find((x) => x.node === node)?.weight
    const k = `${c}/${node}`
    if (base !== undefined && v === base) delete next[k]
    else next[k] = v
  }
  edits.value = next
}

const dirty = computed(() => Object.keys(edits.value).length > 0)

/**
 * 本地预览占比。退出解析的节点不参与分母（与后端一致）。
 *
 * 合并组要按**节点并集**算，不能只看第一条被覆盖的线路：三条线的节点集
 * 未必相同（比如首尔只配在联通里）。只看第一条的话，那些节点会显示成
 * 权重 20 / 占比 0%，看起来像界面算错了。
 */
function shares(g: LineInput): Map<string, number> {
  const m = new Map<string, number>()
  const nodes = nodesOf(g)
  const enabled = nodes.filter((n) => entryOf(g, n)?.dns_enabled)
  const total = enabled.reduce((s, n) => s + weightOf(g, n), 0)
  for (const n of nodes) {
    const on = entryOf(g, n)?.dns_enabled === true
    m.set(n, on && total > 0 ? Math.round((weightOf(g, n) / total) * 1000) / 10 : 0)
  }
  return m
}

/** 参与解析的节点数 / 总节点数。同样按并集算。 */
function participation(g: LineInput): { on: number; total: number } {
  const nodes = nodesOf(g)
  return { on: nodes.filter((n) => entryOf(g, n)?.dns_enabled).length, total: nodes.length }
}

/** 已经分叉的合并组：保存会把它们拉平，得先说。 */
const divergent = computed(() =>
  groups.value.filter((g) => g.supported && isDivergent(weights.value, g)).map((g) => g.name),
)

async function save(): Promise<void> {
  if (!data.value) return
  saving.value = true
  try {
    const body = {
      lines: data.value.lines.map((l) => ({
        code: l.code,
        entries: l.entries.map((e) => ({
          node: e.node,
          weight: edits.value[`${l.code}/${e.node}`] ?? e.weight,
        })),
      })),
    }
    data.value = await http.put<DnsWeightsWire>('/dns/weights', body)
    edits.value = {}
    ui.toast(
      'ok',
      '解析权重已更新',
      configured.value ? '已同步到 DNS 服务商' : '尚未配置服务商，只保存在本地',
    )
  } catch (e) {
    // PUT 是先推服务商、后落库：推失败就不保存，所以本地编辑保留着让人可以改
    ui.toast('warn', '更新失败', e instanceof Error ? e.message : '')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <section class="panel">
    <header class="head">
      <div class="title">DNS 调度</div>
      <div class="sub">
        {{ data?.domain ? `解析域名 ${data.domain}` : '按线路分组' }} ·
        {{ configured ? '保存后立即同步到 DNS 服务商' : '尚未配置服务商' }}
      </div>
      <RouterLink class="mini" to="/settings">DNS 服务商设置</RouterLink>
      <button class="mini" type="button" :disabled="!dirty" @click="load">放弃改动</button>
      <button class="primary" type="button" :disabled="!dirty || saving" @click="save">
        {{ saving ? '保存中…' : configured ? '保存并同步' : '保存到本地' }}
      </button>
    </header>

    <!--
      服务商能力说明。这不是提示性的补充 —— 它决定了下面那些输入框为什么
      长这样。不说的话，人会问「为什么不能分别配电信和联通」。
    -->
    <div v-if="caps && !configured" class="banner warn">
      {{ caps.notes || '尚未配置 DNS 服务商，权重只会保存在本地，不会推到任何地方。' }}
    </div>
    <div v-else-if="caps?.notes" class="banner info">
      <b>{{ caps.kind }}</b> · {{ caps.notes }}
    </div>
    <div v-if="divergent.length" class="banner warn">
      {{ divergent.join('、') }} 下各线路的权重当前并不一致（可能是在别的服务商下配的）。
      保存会把它们拉平成同一个值。
    </div>

    <div v-if="loading && !data" class="hint">正在加载…</div>
    <div v-else-if="error" class="hint error">
      {{ error }}
      <button class="mini" type="button" @click="load">重试</button>
    </div>

    <div v-else-if="data" class="lines">
      <section v-for="g in groups" :key="g.code" class="line" :class="{ off: !g.supported }">
        <header class="line-head">
          <span class="code">{{ g.code }}</span>
          <span class="name">{{ g.name }}</span>
          <span v-if="!g.supported" class="unsupported">当前服务商表达不了这条线路</span>
          <span v-else class="count">
            {{ participation(g).on }} / {{ participation(g).total }} 个节点参与解析
          </span>
        </header>

        <ul class="entries">
          <li
            v-for="n in nodesOf(g)"
            :key="n"
            class="entry"
            :class="{ off: !entryOf(g, n)?.dns_enabled }"
          >
            <span class="node">{{ n }}</span>
            <input
              class="w"
              type="text"
              inputmode="numeric"
              :disabled="!g.supported"
              :value="weightOf(g, n)"
              @input="setWeight(g, n, ($event.target as HTMLInputElement).value)"
            />
            <span class="bar">
              <span
                class="fill"
                :style="{
                  width: `${shares(g).get(n) ?? 0}%`,
                  background: entryOf(g, n)?.dns_enabled ? 'var(--accent)' : 'var(--border-strong)',
                }"
              />
            </span>
            <span class="share">{{ (shares(g).get(n) ?? 0).toFixed(1) }}%</span>
            <span class="st" :class="entryOf(g, n)?.status">{{ entryOf(g, n)?.status }}</span>
            <!--
              退出解析的节点要说清「权重还在、但不承载流量」，
              否则看到 weight 40 / share 0 会以为界面算错了。
            -->
            <span v-if="!entryOf(g, n)?.dns_enabled" class="why">
              {{ entryOf(g, n)?.status === 'down' ? '离线，已自动退出解析' : '已手动暂停解析' }}
              · 权重保留，不参与分流
            </span>
          </li>
        </ul>
      </section>
    </div>
  </section>
</template>

<style scoped>
@import './catalog.css';
.head .sub {
  margin-right: auto;
}
.head .primary {
  padding: 5px 13px;
  border: 1px solid var(--accent);
  border-radius: var(--radius-sm);
  background: var(--accent);
  color: var(--text-on-accent);
  font-size: var(--fs-micro);
  font-weight: var(--weight-semibold);
  cursor: pointer;
  margin-left: var(--space-2);
}
.banner {
  padding: 9px var(--space-4);
  font-size: var(--fs-2xs);
  line-height: 1.7;
  border-bottom: 1px solid var(--border-subtle);
}
.banner.info {
  background: var(--accent-subtle);
  color: var(--accent-text);
}
.banner.warn {
  background: var(--warning-subtle);
  color: var(--warning-text);
}
.lines {
  padding: var(--space-2) 0;
}
.line {
  padding: var(--space-3) var(--space-4);
  border-bottom: 1px solid var(--border-subtle);
}
.line:last-child {
  border-bottom: 0;
}
.line.off {
  opacity: 0.55;
}
.line-head {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
  margin-bottom: var(--space-3);
}
.code {
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  padding: 1px 7px;
  border-radius: var(--radius-full);
  background: var(--surface-sunken);
  color: var(--text-muted);
}
.name {
  font-size: var(--fs-xs);
  font-weight: var(--weight-semibold);
  color: var(--text-strong);
}
.count,
.unsupported {
  margin-left: auto;
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  color: var(--text-faint);
}
.unsupported {
  color: var(--warning-text);
}
.entries {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.entry {
  display: grid;
  grid-template-columns: 130px 64px 1fr 52px 44px;
  align-items: center;
  gap: var(--space-3);
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
}
.entry.off .node,
.entry.off .share {
  color: var(--text-faint);
}
.node {
  color: var(--text-strong);
}
.w {
  padding: 3px 8px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-card);
  color: var(--text-strong);
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  text-align: right;
}
.w:focus {
  border-color: var(--accent);
  outline: none;
}
.w:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.bar {
  height: 6px;
  border-radius: var(--radius-full);
  background: var(--surface-sunken);
  overflow: hidden;
}
.fill {
  display: block;
  height: 100%;
  border-radius: var(--radius-full);
  transition: width var(--dur-fast) var(--ease-out);
}
.share {
  text-align: right;
  color: var(--text-body);
}
.st {
  text-align: right;
  color: var(--success-text);
}
.st.warn {
  color: var(--warning-text);
}
.st.down {
  color: var(--danger-text);
}
.why {
  grid-column: 1 / -1;
  font-size: var(--fs-micro);
  color: var(--warning-text);
  padding-left: 2px;
}
</style>
