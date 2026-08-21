<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { errorText } from '@/api/http'
import { useRoute } from 'vue-router'
import AddNodeModal from '@/components/nodes/AddNodeModal.vue'
import DrainConfirm from '@/components/nodes/DrainConfirm.vue'
import SparkLine from '@/components/console/SparkLine.vue'
import VStatusPill from '@/components/base/VStatusPill.vue'
import { useNodesStore } from '@/stores/nodes'
import { useOverviewStore } from '@/stores/overview'
import { useUiStore } from '@/stores/ui'
import { hbAgeSec } from '@/model'
import { fmtClock, fmtConns, fmtHbAge } from '@/utils/format'
import type { DrainStep } from '@/api/types'

const route = useRoute()
const nodes = useNodesStore()
const overview = useOverviewStore()
const ui = useUiStore()

const open = ref<Set<string>>(new Set())
/** 节点搜索。与命令面板同一套匹配字段：id / 城市 / 服务商 / IP / 线路。 */
const query = ref('')
const drainTarget = ref<string | null>(null)
const addOpen = ref(false)

/** 每秒走一次的本地时钟，只为让「心跳 N 秒前」自己往前跑。 */
const now = ref(Date.now())

/**
 * 解析安排还没到服务商那边。
 *
 * `dns_enabled` 只是个**本地标志位**：它决定归一化里谁参与，但解析记录一个字
 * 都没改 —— 那台机器照旧在解析里。这时把「已退出解析」原样显示出去就是在撒谎，
 * 而且是**常驻**的谎：那句 toast 会消失，这个徽标不会。
 * **消失的那个说了真话，留下的那个不能反着说。**
 *
 * `dnsSync` 为 null 表示还没取到 —— 那时不加限定，宁可少说一句，也不要因为
 * 自己没问到就反过来说节点在撒谎。
 */
const dnsNotSynced = computed(() => nodes.dnsSync?.ok === false)
let ticker: ReturnType<typeof setInterval> | null = null

onMounted(() => {
  if (!nodes.items.length) void nodes.fetchAll().catch(() => {})
  ticker = setInterval(() => (now.value = Date.now()), 1000)
  // ?open=node-id 直达展开，命令面板与总览的「查看日志」都靠它
  const target = route.query.open
  if (typeof target === 'string') expand(target)
})

onBeforeUnmount(() => {
  if (ticker) clearInterval(ticker)
  ticker = null
})

function expand(id: string): void {
  const next = new Set(open.value)
  if (next.has(id)) {
    next.delete(id)
  } else {
    next.add(id)
    if (!nodes.logs[id]) void nodes.fetchLogs(id).catch(() => {})
  }
  open.value = next
}

const rows = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return nodes.items
  return nodes.items.filter((n) =>
    `${n.id} ${n.city} ${n.vendor} ${n.ip} ${n.line}`.toLowerCase().includes(q),
  )
})

async function onPush(id: string): Promise<void> {
  try {
    const r = await nodes.pushOne(id)
    ui.toast('ok', `已向 ${id} 重推基线`, r.cfg_version)
    void nodes.fetchLogs(id).catch(() => {})
  } catch (e) {
    ui.toast('warn', '重推失败', errorText(e, ''))
  }
}

async function onDns(id: string, enabled: boolean): Promise<void> {
  try {
    const r = await nodes.toggleDns(id, enabled)
    /*
     * 没同步到服务商时，标题**不能**说「已暂停解析」。
     *
     * 那句话等于声称流量已经不走这台机器了，而它照旧在解析里 —— 人可能正是在
     * 故障里点的这个开关。用 danger 而不是 warn：暂停成功本身就是 warn，
     * 两者同色的话，一眼扫过去分不出「做成了」和「什么也没发生」。
     */
    if (!r.dns_synced) {
      ui.toast('danger', `${id} 的解析没有变动`, r.detail)
      return
    }
    ui.toast(
      enabled ? 'ok' : 'warn',
      enabled ? `${id} 已恢复解析` : `${id} 已暂停解析`,
      r.detail,
    )
  } catch (e) {
    ui.toast('warn', '操作失败', errorText(e, ''))
  }
}

async function onProbe(id: string): Promise<void> {
  try {
    const r = await nodes.probe(id)
    ui.toast(
      r.caddy_admin ? 'ok' : 'warn',
      `${id} 可达 · rtt ${r.rtt_ms}ms`,
      r.caddy_admin ? 'Caddy Admin 正常' : 'Caddy Admin 不可达 —— 隧道通但 Caddy 可能已挂',
    )
    if (!open.value.has(id)) expand(id)
  } catch (e) {
    ui.toast('danger', '探活失败', errorText(e, ''))
  }
}

/**
 * 下线的三步结果**不进 toast**。
 *
 * 每一步的 detail 是人接下来那个决定的判据 —— 「解析缓存未过期前仍可能有新连接
 * 进来」是「已排空」这句话的边界，「还剩 214 条连接未结束」和「还剩 2 条」要做的
 * 是两个不同的决定。三句话拼成一行塞进一个会自己消失的 toast，等于没说。
 *
 * 所以结果留在弹层里，由人自己关掉 —— 跟回滚的 skipped 一样：**要人看见的东西
 * 不能自己走掉**。
 */
const drainResult = ref<DrainStep[] | null>(null)

async function onDrain(): Promise<void> {
  const id = drainTarget.value
  if (!id) return
  try {
    const r = await nodes.drain(id)
    drainResult.value = r.steps
  } catch (e) {
    ui.toast('warn', '下线失败', errorText(e, ''))
    drainTarget.value = null
  }
}

function closeDrain(): void {
  drainTarget.value = null
  drainResult.value = null
}

async function onRejoin(id: string): Promise<void> {
  try {
    const r = await nodes.rejoin(id)
    // detail 原样给出：它说的是「解析仍是关闭的」，而人多半以为重新上线就恢复了
    ui.toast('ok', `${id} 已重新上线`, r.detail)
  } catch (e) {
    ui.toast('warn', '重新上线失败', errorText(e, ''))
  }
}

const drainNode = computed(() =>
  drainTarget.value ? nodes.byId.get(drainTarget.value) : undefined,
)

const LEVEL_COLOR: Record<string, string> = {
  debug: 'var(--text-faint)',
  info: 'var(--text-muted)',
  warn: 'var(--warning-text)',
  error: 'var(--danger-text)',
}
</script>

<template>
  <section class="panel">
    <header class="head">
      <div class="title">边缘节点</div>
      <div class="sub">
        心跳间隔 3s · gRPC 长连接 ·
        {{ query ? `${rows.length} / ${nodes.items.length}` : `共 ${nodes.items.length}` }} 台
      </div>
      <input
        v-model="query"
        class="search"
        type="search"
        placeholder="搜节点名、城市、服务商、IP、线路"
        aria-label="搜索节点"
      />
      <button class="primary" type="button" @click="addOpen = true">添加节点</button>
    </header>

    <div v-if="nodes.loading && !rows.length" class="hint">正在加载…</div>
    <div v-else-if="nodes.error" class="hint error">
      {{ nodes.error }}
      <button class="mini" type="button" @click="nodes.fetchAll()">重试</button>
    </div>
    <div v-else-if="!nodes.items.length" class="hint">
      还没有边缘节点。用「添加节点」签发一次性 Token 接入第一台。
    </div>
    <div v-else-if="!rows.length" class="hint">
      没有匹配「{{ query }}」的节点。
      <button class="mini" type="button" @click="query = ''">清除搜索</button>
    </div>

    <ul v-else class="rows">
      <li v-for="n in rows" :key="n.id" class="row" :class="{ open: open.has(n.id) }">
        <button class="lane" type="button" @click="expand(n.id)">
          <span class="ident">
            <span class="id">{{ n.id }}</span>
            <span class="where">{{ n.city }} · {{ n.vendor }}</span>
            <span class="line">{{ n.line }} · {{ n.ip }}</span>
          </span>

          <VStatusPill :status="n.status" />

          <span class="spark"><SparkLine :values="n.cpuSeries" :status="n.status" :height="26" /></span>

          <span class="metrics">
            <span>CPU <b>{{ n.cpu.toFixed(1) }}%</b></span>
            <span>内存 <b>{{ n.mem.toFixed(1) }}%</b></span>
            <span>连接 <b>{{ fmtConns(n.conns) }}</b></span>
            <span>心跳 <b>{{ fmtHbAge(hbAgeSec(n, now)) }}</b></span>
          </span>

          <span class="flags">
            <!--
              下线与离线**各占一格，永不合并**。
              status 是观察（主控没收到心跳），drained_at 是意图（人按了下线）。
              一台节点可以「已下线且在线」，也可以「未下线但离线」——前者是
              「我关的」，后者是故障。合成一个徽标，运维半夜分不清该不该起床
              （CONTEXT.md、ADR-0014）。左边那个 VStatusPill 就是 status 那一格。
            -->
            <span v-if="n.drainedAt" class="flag drained" :title="`下线于 ${fmtClock(n.drainedAt)}`">
              已下线（人为）
            </span>
            <span v-if="!n.dnsEnabled" class="flag warn">
              {{ dnsNotSynced ? '已标记退出（解析未变）' : '已退出解析' }}
            </span>
            <span v-if="n.drift" class="flag warn">未收到最近下发</span>
          </span>

          <span class="chev">{{ open.has(n.id) ? '收起' : '展开' }}</span>
        </button>

        <div v-if="open.has(n.id)" class="detail">
          <div class="cols">
            <div class="col">
              <div class="col-title">生效配置</div>
              <dl>
                <dt>配置版本</dt>
                <dd class="mono">
                  {{ n.cfgVersion }}
                  <span v-if="n.drift" class="warn">· 未收到最近一次下发</span>
                </dd>
                <dt>基线</dt>
                <dd class="mono">{{ overview.baseline || '—' }}</dd>
                <dt>生效路由 / 规则</dt>
                <dd class="mono">{{ n.routes }} / {{ n.rules }}</dd>
                <dt>DNS 解析</dt>
                <dd :class="{ warn: !n.dnsEnabled }">
                  {{ n.dnsEnabled ? '正常参与解析' : '已暂停' }}
                  <!-- detail 是后端给的原话，不要自己编：它区分「没配服务商」和「同步失败了」 -->
                  <span v-if="dnsNotSynced" class="warn">· {{ nodes.dnsSync!.detail }}</span>
                </dd>
              </dl>

              <div class="col-title">Caddy Admin</div>
              <!--
                隧道可达性与 Caddy Admin 可达性分开报（契约 §4）：
                隧道通而 Admin 不通 = Caddy 挂了但 Agent 还活着，
                这两种故障的处置完全不同。
              -->
              <p v-if="!nodes.probes[n.id]" class="muted small">
                还没探活过。点「立即探活」会同时报隧道 rtt 与 Admin 可达性。
              </p>
              <p v-else class="small">
                隧道 <b class="mono">{{ nodes.probes[n.id]!.rtt_ms }}ms</b> ·
                Admin
                <b :class="nodes.probes[n.id]!.caddy_admin ? 'okc' : 'warn'">
                  {{ nodes.probes[n.id]!.caddy_admin ? '可达' : '不可达' }}
                </b>
              </p>
            </div>

            <div class="col logs">
              <div class="col-title">Agent 日志</div>
              <ol v-if="nodes.logs[n.id]?.length" class="loglist">
                <li v-for="(l, i) in nodes.logs[n.id]!.slice(0, 12)" :key="i">
                  <span class="t">{{ fmtClock(l.at) }}</span>
                  <span :style="{ color: LEVEL_COLOR[l.level] }">{{ l.msg }}</span>
                </li>
              </ol>
              <p v-else class="muted small">暂无日志。</p>
            </div>
          </div>

          <div class="actions">
            <button class="mini" type="button" :disabled="!!nodes.busy[n.id]" @click="onPush(n.id)">
              {{ nodes.busy[n.id] === '重推中' ? '重推中…' : '重推配置' }}
            </button>
            <!--
              已下线的节点开解析后端会回 2001（先「重新上线」）。这里直接禁用并
              说明，而不是让人点了再被拒 —— 「置灰只是把拒绝提前」。
              关解析不拒，所以只在「要开」的方向禁用。
            -->
            <button
              class="mini"
              type="button"
              :disabled="!!nodes.busy[n.id] || (!!n.drainedAt && !n.dnsEnabled)"
              :title="!!n.drainedAt && !n.dnsEnabled ? '该节点已被下线，先「重新上线」再恢复解析' : ''"
              @click="onDns(n.id, !n.dnsEnabled)"
            >
              {{ n.dnsEnabled ? '暂停解析' : '恢复解析' }}
            </button>
            <button class="mini" type="button" :disabled="!!nodes.busy[n.id]" @click="onProbe(n.id)">
              {{ nodes.busy[n.id] === '探活中' ? '探活中…' : '立即探活' }}
            </button>
            <!--
              下线在没有「重新上线」时是个单程操作 —— 误点的代价不对称，
              而唯一的出路是去改数据库。两个按钮在同一处，人看得见回头路。
            -->
            <button
              v-if="n.drainedAt"
              class="mini"
              type="button"
              :disabled="!!nodes.busy[n.id]"
              @click="onRejoin(n.id)"
            >
              {{ nodes.busy[n.id] === '重新上线中' ? '重新上线中…' : '重新上线' }}
            </button>
            <button v-else class="mini danger" type="button" @click="drainTarget = n.id">
              下线节点
            </button>
          </div>
        </div>
      </li>
    </ul>
  </section>

  <DrainConfirm
    v-if="drainNode"
    :node-id="drainNode.id"
    :conns="drainNode.conns"
    :busy="!!nodes.busy[drainNode.id]"
    :result="drainResult"
    @cancel="closeDrain"
    @confirm="onDrain"
  />
  <AddNodeModal v-if="addOpen" @close="addOpen = false" />
</template>

<style scoped>
@import './catalog.css';
.head .primary {
  padding: 5px 13px;
  border: 1px solid var(--accent);
  border-radius: var(--radius-sm);
  background: var(--accent);
  color: var(--text-on-accent);
  font-size: var(--fs-micro);
  font-weight: var(--weight-semibold);
  cursor: pointer;
}
.head .sub {
  margin-right: auto;
}
.search {
  width: 230px;
  padding: 5px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-card);
  color: var(--text-strong);
  font-size: var(--fs-micro);
}
.search:focus {
  border-color: var(--accent);
  outline: none;
}
.rows {
  list-style: none;
  margin: 0;
  padding: 0;
}
.row {
  border-bottom: 1px solid var(--border-subtle);
}
.row:last-child {
  border-bottom: 0;
}
.row.open {
  background: var(--surface-sunken);
}
.lane {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  width: 100%;
  padding: 11px var(--space-4);
  border: 0;
  background: transparent;
  cursor: pointer;
  text-align: left;
}
.lane:hover {
  background: var(--surface-sunken);
}
.ident {
  min-width: 190px;
}
.id {
  display: block;
  font-family: var(--font-mono);
  font-size: var(--fs-xs);
  font-weight: var(--weight-semibold);
  color: var(--text-strong);
}
.where {
  display: block;
  font-size: var(--fs-micro);
  color: var(--text-muted);
}
.line {
  display: block;
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  color: var(--text-faint);
}
.spark {
  width: 120px;
  flex: none;
}
.metrics {
  display: flex;
  gap: var(--space-4);
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  color: var(--text-muted);
  white-space: nowrap;
}
.metrics b {
  color: var(--text-strong);
  font-weight: var(--weight-semibold);
}
.flags {
  display: flex;
  gap: var(--space-1-5);
  margin-left: auto;
}
.flag {
  padding: 1px 8px;
  border-radius: var(--radius-full);
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
}
.flag.drained {
  border-color: var(--text-muted);
  color: var(--text-muted);
}
.flag.warn {
  background: var(--warning-subtle);
  color: var(--warning-text);
}
.chev {
  font-size: var(--fs-micro);
  color: var(--text-faint);
  flex: none;
}
.detail {
  padding: 0 var(--space-4) var(--space-4);
}
.cols {
  display: grid;
  grid-template-columns: minmax(240px, 1fr) 1.4fr;
  gap: var(--space-5);
}
.col-title {
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-muted);
  font-weight: var(--weight-semibold);
  margin: var(--space-2) 0 var(--space-1-5);
}
dl {
  margin: 0;
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 5px var(--space-3);
  font-size: var(--fs-2xs);
}
dt {
  color: var(--text-muted);
  white-space: nowrap;
}
dd {
  margin: 0;
  color: var(--text-strong);
}
.loglist {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 3px;
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  max-height: 190px;
  overflow-y: auto;
}
.loglist .t {
  color: var(--text-faint);
  margin-right: var(--space-2);
}
.small {
  font-size: var(--fs-2xs);
  margin: 0;
}
.okc {
  color: var(--success-text);
}
.warn {
  color: var(--warning-text);
}
.actions {
  display: flex;
  gap: var(--space-2);
  margin-top: var(--space-4);
}
.mini.danger {
  border-color: var(--danger);
  color: var(--danger-text);
}
</style>
