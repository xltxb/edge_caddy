<script setup lang="ts">
import { onMounted, ref } from 'vue'
import StatusPill from '@/components/base/StatusPill.vue'
import { useNodesStore } from '@/stores/nodes'
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

    <section class="card">
      <div class="thead mono">
        <span>节点</span><span>城市</span><span>负载</span><span>配置版本</span><span style="text-align:right">状态</span>
      </div>
      <div v-if="nodes.loadError" class="pad err">{{ nodes.loadError }}</div>
      <div v-else-if="!nodes.nodes.length" class="pad empty">
        还没有节点接入。点右上角「添加节点」拿到安装命令。
      </div>
      <div v-for="n in nodes.nodes" :key="n.id" class="row">
        <span class="mono id">{{ n.id }}</span>
        <span>{{ n.city || '—' }}</span>
        <span class="mono load">CPU {{ (n.cpu ?? 0).toFixed(1) }}% · MEM {{ (n.mem ?? 0).toFixed(1) }}%</span>
        <span class="mono cfg">
          {{ n.cfg || '—' }}
          <StatusPill v-if="n.drifted" tone="warn" text="待同步" />
        </span>
        <span style="text-align:right">
          <StatusPill :tone="toneOf(n.status)" :text="labelOf(n.status)" />
        </span>
      </div>
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
.thead, .row { display: grid; grid-template-columns: minmax(0,1.2fr) 90px minmax(0,1fr) minmax(0,1fr) 110px; gap: 14px; padding: 9px 16px; border-bottom: 1px solid var(--border-subtle); align-items: center; }
.thead { font-size: 10px; letter-spacing: .08em; text-transform: uppercase; color: var(--text-faint); font-weight: 600; }
.row:last-child { border-bottom: 0; }
.id { font-size: 12.5px; font-weight: 600; color: var(--text-strong); }
.load, .cfg { font-size: 11.5px; color: var(--text-muted); display: flex; align-items: center; gap: 7px; }
</style>
