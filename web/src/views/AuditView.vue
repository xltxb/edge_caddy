<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import StatusPill from '@/components/base/StatusPill.vue'
import { get } from '@/api/http'

interface AuditLog {
  id: number
  operator: string
  action: string
  target: string
  src_ip: string
  result: string
  at: string
}

const logs = ref<AuditLog[]>([])
const operator = ref('')
const err = ref('')

async function load() {
  err.value = ''
  try {
    const q = operator.value ? `?operator=${encodeURIComponent(operator.value)}` : ''
    logs.value = (await get<{ logs: AuditLog[] }>(`/audit${q}`)).logs ?? []
  } catch (e) {
    err.value = (e as Error).message
  }
}
onMounted(load)

const operators = computed(() => [...new Set(logs.value.map((l) => l.operator))].filter(Boolean).sort())
/** 失败的登录单独提示——它是排查爆破的第一手线索（PRD §5）。 */
const failedLogins = computed(() =>
  logs.value.filter((l) => l.result === 'fail' && l.action.includes('login')).length,
)

function fmt(t: string) {
  return t ? t.replace('T', ' ').slice(0, 19) : '—'
}
</script>

<template>
  <div class="wrap">
    <section v-if="failedLogins" class="card pad warn">
      ⚠ 最近记录里有 <b>{{ failedLogins }}</b> 次登录失败。若非本人操作，请检查主控是否暴露在公网。
    </section>

    <section class="card head">
      <span>审计日志</span>
      <select v-model="operator" class="inp" @change="load">
        <option value="">全部操作人</option>
        <option v-for="o in operators" :key="o" :value="o">{{ o }}</option>
      </select>
    </section>

    <section v-if="err" class="card pad e">{{ err }}</section>
    <section class="card">
      <div class="thead mono">
        <span>时间</span><span>操作人</span><span>动作</span><span>对象</span><span>来源 IP</span><span>结果</span>
      </div>
      <div v-if="!logs.length" class="pad empty">没有记录。只读请求不会留痕。</div>
      <div v-for="l in logs" :key="l.id" class="row" :class="{ bad: l.result === 'fail' }">
        <span class="mono sub">{{ fmt(l.at) }}</span>
        <span class="mono op">{{ l.operator }}</span>
        <span class="mono">{{ l.action }}</span>
        <span class="mono sub">{{ l.target || '—' }}</span>
        <span class="mono sub">{{ l.src_ip }}</span>
        <span><StatusPill :tone="l.result === 'ok' ? 'ok' : 'danger'" :text="l.result === 'ok' ? '成功' : '失败'" /></span>
      </div>
    </section>
    <p class="note">只记录写操作。把只读也记下来会让流水被巡检刷满，真正重要的那几条就淹没了。</p>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 12px; }
.card { background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; }
.head { display: flex; align-items: center; justify-content: space-between; padding: 11px 16px; font-weight: 600; color: var(--text-strong); }
.pad { padding: 14px 16px; }
.warn { background: var(--warning-subtle); color: var(--warning-text); font-size: 12.5px; line-height: 1.7; }
.e { color: var(--danger-text); font-size: 13px; }
.empty { color: var(--text-muted); font-size: 13px; }
.inp { padding: 5px 9px; border-radius: 8px; font-size: 12.5px; border: 1px solid var(--border-default); background: var(--surface-card); color: var(--text-strong); }
.thead, .row { display: grid; grid-template-columns: 150px 90px minmax(0,1fr) minmax(0,1fr) 120px 80px; gap: 12px; padding: 8px 16px; border-bottom: 1px solid var(--border-subtle); align-items: center; font-size: 11.5px; }
.thead { font-size: 10px; letter-spacing: .08em; text-transform: uppercase; color: var(--text-faint); font-weight: 600; }
.row:last-child { border-bottom: 0; }
.row.bad { background: var(--danger-subtle); }
.op { font-weight: 600; color: var(--text-strong); }
.sub { color: var(--text-muted); }
.note { margin: 0; font-size: 11.5px; color: var(--text-faint); }
</style>
