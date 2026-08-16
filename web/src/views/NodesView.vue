<script setup lang="ts">
import { onMounted, ref } from 'vue'
import StatusPill from '@/components/base/StatusPill.vue'
import { useNodesStore, type NodeVerb } from '@/stores/nodes'
import { post } from '@/api/http'

const nodes = useNodesStore()
const token = ref<{ token: string; install: string; expires_at: string } | null>(null)
const tokenErr = ref('')
const busy = ref(false)

onMounted(() => nodes.load())

async function issueToken() {
  busy.value = true
  tokenErr.value = ''
  try {
    token.value = await post<{ token: string; install: string; expires_at: string }>('/nodes/token')
  } catch (e) {
    tokenErr.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

/** 行内操作与命令面板走同一条路径：都调 nodes.runOp。 */
const OPS: { verb: NodeVerb; label: string; danger?: boolean }[] = [
  { verb: 'probe', label: '探活' },
  { verb: 'push', label: '重推' },
  { verb: 'drain', label: '下线', danger: true },
]

function toneOf(status: string): 'ok' | 'warn' | 'danger' {
  return status === 'ok' ? 'ok' : status === 'warn' ? 'warn' : 'danger'
}
function labelOf(status: string): string {
  return status === 'ok' ? '在线' : status === 'warn' ? '异常' : '离线'
}
</script>

<template>
  <div class="wrap">
    <section class="card head">
      <span>边缘节点</span>
      <button class="btn" :disabled="busy" @click="issueToken">
        {{ busy ? '签发中…' : '添加节点' }}
      </button>
    </section>

    <section v-if="token" class="card pad">
      <div class="t">在目标机器上执行（Token 一次性，{{ token.expires_at }} 前有效）</div>
      <!-- Token 走环境变量而非命令行参数：命令行参数会出现在 ps 输出里 -->
      <pre class="cmd mono">{{ token.install }}</pre>
    </section>
    <section v-if="tokenErr" class="card pad err">{{ tokenErr }}</section>

    <section v-if="nodes.opLog.length" class="card pad oplog">
      <div v-for="(r, i) in nodes.opLog.slice(0, 5)" :key="i" class="op-row" :class="{ bad: !r.ok }">
        <span class="mono">{{ r.node }}</span>
        <span>{{ r.verb }}</span>
        <span class="detail">{{ r.detail }}</span>
      </div>
    </section>

    <section class="card">
      <div class="thead mono">
        <span>节点</span><span>城市</span><span>负载</span><span>配置版本</span><span>操作</span><span style="text-align:right">状态</span>
      </div>
      <div v-if="nodes.loadError" class="pad err">{{ nodes.loadError }}</div>
      <div v-else-if="!nodes.nodes.length" class="pad empty">
        还没有节点接入。点右上角「添加节点」拿到安装命令。
      </div>
      <template v-for="n in nodes.nodes" :key="n.id">
      <div class="row" :data-node="n.id">
        <span class="mono id">
          <button class="tw" :aria-expanded="nodes.expanded === n.id" @click="nodes.expand(n.id)">
            {{ nodes.expanded === n.id ? '▾' : '▸' }}
          </button>
          {{ n.id }}
        </span>
        <span>{{ n.city || '—' }}</span>
        <span class="mono load">CPU {{ (n.cpu ?? 0).toFixed(1) }}% · MEM {{ (n.mem ?? 0).toFixed(1) }}%</span>
        <span class="mono cfg">
          {{ n.cfg || '—' }}
          <StatusPill v-if="n.drifted" tone="warn" text="待同步" />
        </span>
        <span class="ops">
          <button
            v-for="o in OPS"
            :key="o.verb"
            class="op"
            :class="{ danger: o.danger }"
            :disabled="nodes.busyOp === `${o.verb}:${n.id}`"
            @click="o.verb === 'drain' ? nodes.askDrain(n.id) : nodes.runOp(o.verb, n.id)"
          >
            {{ nodes.busyOp === `${o.verb}:${n.id}` ? '…' : o.label }}
          </button>
        </span>
        <span style="text-align:right">
          <StatusPill :tone="toneOf(n.status)" :text="labelOf(n.status)" />
        </span>
      </div>

      <!-- 展开区的三样信息来自同一次探活回报，不是三次分别取的 -->
      <div v-if="nodes.expanded === n.id" class="exp" :data-detail="n.id">
        <template v-if="nodes.busyOp === `expand:${n.id}`">探活中…</template>
        <template v-else-if="nodes.detail[n.id]?.error" >
          <span class="err">探活失败：{{ nodes.detail[n.id].error }}</span>
        </template>
        <template v-else-if="nodes.detail[n.id]">
          <div class="kv"><span>生效配置</span><b class="mono">{{ nodes.detail[n.id].cfgVersion || '尚未生效' }}</b></div>
          <div class="kv">
            <span>Caddy Admin</span>
            <b :class="{ bad: !nodes.detail[n.id].caddyOk }">
              {{ nodes.detail[n.id].caddyOk ? '可达' : '不可达' }} · {{ nodes.detail[n.id].caddyDetail }}
            </b>
          </div>
          <div class="kv"><span>往返时延</span><b class="mono">{{ nodes.detail[n.id].rttMs }}ms</b></div>
          <div class="lg">Agent 最近日志</div>
          <pre class="logs mono">{{ (nodes.detail[n.id].logs ?? []).slice(-20).join('\n') || '（暂无）' }}</pre>
        </template>
      </div>
      </template>
    </section>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 14px; }
.card { background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; }
.head { display: flex; align-items: center; justify-content: space-between; padding: 12px 16px; font-weight: 600; color: var(--text-strong); }
.pad { padding: 14px 16px; }
.btn { padding: 6px 13px; border: 0; border-radius: 8px; cursor: pointer; background: var(--accent); color: var(--text-on-accent); font-size: 12.5px; font-weight: 600; }
.btn:hover:not(:disabled) { background: var(--accent-hover); }
.t { font-size: 11.5px; color: var(--text-muted); margin-bottom: 7px; }
.cmd { margin: 0; padding: 10px 12px; border-radius: 9px; background: var(--surface-sunken); font-size: 11.5px; line-height: 1.6; overflow-x: auto; color: var(--text-strong); }
.err { color: var(--danger-text); font-size: 13px; }
.empty { color: var(--text-muted); font-size: 13px; }
.thead, .row { display: grid; grid-template-columns: minmax(0,1.2fr) 80px minmax(0,1fr) minmax(0,.9fr) 150px 92px; gap: 14px; padding: 9px 16px; border-bottom: 1px solid var(--border-subtle); align-items: center; }
.thead { font-size: 10px; letter-spacing: .08em; text-transform: uppercase; color: var(--text-faint); font-weight: 600; }
.row:last-child { border-bottom: 0; }
.id { font-size: 12.5px; font-weight: 600; color: var(--text-strong); }
.tw { background: none; border: 0; cursor: pointer; color: var(--text-faint); padding: 0 4px 0 0; font-size: 11px; }
.exp { padding: 10px 16px 14px 34px; border-bottom: 1px solid var(--border-subtle); background: var(--surface-sunken); font-size: 12px; color: var(--text-body); }
.kv { display: grid; grid-template-columns: 96px 1fr; gap: 10px; padding: 2px 0; }
.kv span { color: var(--text-faint); }
.kv b.bad { color: var(--danger-text); }
.lg { margin-top: 9px; font-size: 10px; letter-spacing: .08em; text-transform: uppercase; color: var(--text-faint); font-weight: 600; }
.logs { margin: 5px 0 0; padding: 9px 11px; border-radius: 8px; background: var(--surface-card); border: 1px solid var(--border-subtle); font-size: 11px; line-height: 1.6; max-height: 190px; overflow: auto; white-space: pre-wrap; }
.ops { display: flex; gap: 6px; }
.op { padding: 3px 9px; border: 1px solid var(--border-subtle); border-radius: 7px; background: transparent; color: var(--text-body); font-size: 11.5px; cursor: pointer; }
.op:hover:not(:disabled) { background: var(--surface-sunken); }
.op:disabled { opacity: .5; cursor: default; }
.op.danger { color: var(--danger-text); }
.oplog { display: flex; flex-direction: column; gap: 5px; }
.op-row { display: grid; grid-template-columns: 150px 60px 1fr; gap: 10px; font-size: 11.5px; color: var(--text-muted); }
.op-row.bad .detail { color: var(--danger-text); }
.load, .cfg { font-size: 11.5px; color: var(--text-muted); display: flex; align-items: center; gap: 7px; }
</style>
