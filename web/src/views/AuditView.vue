<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { http, errorText } from '@/api/http'
import { orDash } from '@/model'
import type { AuditWire, Paged } from '@/api/types'

/**
 * 审计日志 —— 排障链条上「谁改的」那一半。
 *
 * `action` 的取值由后端产生、这里**原样显示**，所以那些字符串是契约的一部分
 * （契约 §5 有取值表，一律用术语表的「下发」而不是推送 / 发布）。
 */
const items = ref<AuditWire[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const operator = ref('all')

/**
 * nextBeforeId 是**还有没有下一页**（契约 §0.4 的 cursor 分页）。
 *
 * 这个字段在 types.ts 里声明过之后一直没人读，于是这一页只渲染第一页
 * 而自称「全部写操作与登录记录」。正常使用一段时间后第一页被日常写操作
 * 填满，暴力破解留下的失败登录被挤出去 —— 横幅归零，页面还说「全部」
 * （issue #49）。
 *
 * null 表示到底了。只有那时这一页才说得出「全部」。
 */
const nextBeforeId = ref<number | null>(null)

async function fetchPage(beforeID: number | null): Promise<Paged<AuditWire>> {
  const q = new URLSearchParams()
  if (operator.value !== 'all') q.set('operator', operator.value)
  if (beforeID !== null) q.set('before_id', String(beforeID))
  const qs = q.toString()
  return http.get<Paged<AuditWire>>(`/audit${qs ? `?${qs}` : ''}`)
}

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    const page = await fetchPage(null)
    items.value = page.items
    nextBeforeId.value = page.next_before_id
  } catch (e) {
    error.value = errorText(e, '加载审计日志失败')
  } finally {
    loading.value = false
  }
}

async function loadMore(): Promise<void> {
  if (nextBeforeId.value === null || loading.value) return
  loading.value = true
  error.value = null
  try {
    const page = await fetchPage(nextBeforeId.value)
    items.value = [...items.value, ...page.items]
    nextBeforeId.value = page.next_before_id
  } catch (e) {
    error.value = errorText(e, '加载更多审计日志失败')
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch(operator, load)

/**
 * 副标题说的是**这一页此刻真的拿到了什么**。
 *
 * 「全部」是一句承诺，只有翻到底之后才兑现得了。没到底时说出条数与
 * 「还有更早的」，人就知道自己看到的是一个窗口而不是全景。
 */
const scope = computed(() =>
  nextBeforeId.value === null
    ? `全部写操作与登录记录（${items.value.length} 条）· 倒序`
    : `已加载最近 ${items.value.length} 条，还有更早的 · 倒序`,
)

/** 操作人列表从当前结果里取，切到「全部」时才重算。 */
const operators = ref<string[]>([])
watch(items, (v) => {
  if (operator.value === 'all') operators.value = [...new Set(v.map((a) => a.operator))]
})

/**
 * 失败的**登录**尝试单独提示（契约 §10）。
 * 它混在流水里很难被注意到，而它恰恰是唯一一类可能来自外部的信号。
 */
const failedLogins = computed(() =>
  // action 的取值见契约 §5 的术语表，是「登录」而不是「登录控制台」。
  // 自己编一个不在表里的值，这条提示就永远不会触发 —— 而它恰恰是唯一一类
  // 可能来自外部的信号。
  items.value.filter((a) => a.action === '登录' && a.result === 'fail'),
)

const RESULT: Record<string, { text: string; cls: string }> = {
  ok: { text: '成功', cls: 'ok' },
  fail: { text: '失败', cls: 'danger' },
  partial: { text: '部分成功', cls: 'warn' },
}

function stamp(at: string): string {
  const d = new Date(at)
  if (Number.isNaN(d.getTime())) return at
  const p = (n: number) => String(n).padStart(2, '0')
  const today = new Date()
  const sameDay = d.toDateString() === today.toDateString()
  const hm = `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
  return sameDay ? hm : `${p(d.getMonth() + 1)}-${p(d.getDate())} ${hm}`
}
</script>

<template>
  <section class="panel">
    <header class="head">
      <div class="title">审计日志</div>
      <div class="sub">{{ scope }}</div>
      <select v-model="operator" class="select">
        <option value="all">全部操作人</option>
        <option v-for="o in operators" :key="o" :value="o">{{ o }}</option>
      </select>
    </header>

    <div v-if="failedLogins.length" class="banner danger" data-test="failed-logins">
      {{ nextBeforeId === null ? '有' : '已加载的记录里有' }}
      {{ failedLogins.length }} 次失败的登录尝试，最近一次来自
      <span class="mono">{{ orDash(failedLogins[0]!.src_ip) }}</span>。
    </div>

    <div v-if="loading && !items.length" class="hint">正在加载…</div>
    <div v-else-if="error" class="hint error">
      {{ error }}
      <button class="mini" type="button" @click="load">重试</button>
    </div>
    <div v-else-if="!items.length" class="hint">这个筛选下没有记录。</div>

    <table v-else class="table">
      <thead>
        <tr>
          <th>时间</th>
          <th>操作人</th>
          <th>动作</th>
          <th>对象</th>
          <th>来源 IP</th>
          <th>结果</th>
        </tr>
      </thead>
      <tbody>
        <tr
          v-for="a in items"
          :key="a.id"
          :class="{ bad: a.result === 'fail' }"
        >
          <td class="mono muted">{{ stamp(a.at) }}</td>
          <td class="mono">{{ a.operator }}</td>
          <td class="strong">{{ a.action }}</td>
          <td class="mono muted">{{ orDash(a.target) }}</td>
          <td class="mono muted">{{ orDash(a.src_ip) }}</td>
          <td>
            <span class="tag" :class="RESULT[a.result]!.cls">{{ RESULT[a.result]!.text }}</span>
            <span v-if="a.detail" class="muted small"> · {{ a.detail }}</span>
          </td>
        </tr>
      </tbody>
    </table>

    <div v-if="nextBeforeId !== null && items.length" class="more">
      <button class="mini" type="button" data-test="load-more" :disabled="loading" @click="loadMore">
        {{ loading ? '加载中…' : '加载更早的记录' }}
      </button>
    </div>
  </section>
</template>

<style scoped>
@import './catalog.css';
.head .sub {
  margin-right: auto;
}
.select {
  padding: 4px 9px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-card);
  color: var(--text-strong);
  font-size: var(--fs-micro);
  font-family: var(--font-mono);
}
.banner {
  padding: 9px var(--space-4);
  font-size: var(--fs-xs);
  border-bottom: 1px solid var(--border-subtle);
}
.banner.danger {
  background: var(--danger-subtle);
  color: var(--danger-text);
}
.table tbody tr.bad td:first-child {
  box-shadow: inset 2px 0 0 var(--danger);
}
.small {
  font-size: var(--fs-micro);
}
</style>
