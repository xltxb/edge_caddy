<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { http } from '@/api/http'
import type { DnsWeightsWire } from '@/api/types'
import { useUiStore } from '@/stores/ui'

/**
 * DNS 调度。
 *
 * 关键是 **weight 与 share 不是一回事**（契约 §8）：weight 是你配的值，
 * share 是实际占比。退出解析的节点 share 为 0，它的权重在该线路内的其余节点
 * 之间重新归一化。把两者混成一个数字，就说不清「我配了 40，为什么它没在扛流量」。
 */
const ui = useUiStore()

const data = ref<DnsWeightsWire | null>(null)
const loading = ref(false)
const saving = ref(false)
const error = ref<string | null>(null)
/** 本地编辑中的权重：`${line}/${node}` → weight。 */
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

const key = (line: string, node: string) => `${line}/${node}`

function weightOf(line: string, node: string, base: number): number {
  const k = key(line, node)
  return edits.value[k] ?? base
}

function setWeight(line: string, node: string, base: number, raw: string): void {
  const v = Math.max(0, Math.round(Number(raw) || 0))
  const k = key(line, node)
  const next = { ...edits.value }
  if (v === base) delete next[k]
  else next[k] = v
  edits.value = next
}

const dirty = computed(() => Object.keys(edits.value).length > 0)

/**
 * 本地预览占比 —— 改输入框时占比条要立刻跟着动，不能等保存往返。
 * 算法必须与后端一致：退出解析的节点不参与分母。
 */
function localShare(lineCode: string, entries: DnsWeightsWire['lines'][number]['entries']): Map<string, number> {
  const enabled = entries.filter((e) => e.dns_enabled)
  const total = enabled.reduce((s, e) => s + weightOf(lineCode, e.node, e.weight), 0)
  const m = new Map<string, number>()
  for (const e of entries) {
    const w = weightOf(lineCode, e.node, e.weight)
    m.set(e.node, e.dns_enabled && total > 0 ? Math.round((w / total) * 1000) / 10 : 0)
  }
  return m
}

async function save(): Promise<void> {
  if (!data.value) return
  saving.value = true
  try {
    const body = {
      lines: data.value.lines.map((l) => ({
        code: l.code,
        entries: l.entries.map((e) => ({ node: e.node, weight: weightOf(l.code, e.node, e.weight) })),
      })),
    }
    data.value = await http.put<DnsWeightsWire>('/dns/weights', body)
    edits.value = {}
    ui.toast('ok', '解析权重已更新', '已同步到 DNS 服务商')
  } catch (e) {
    // PUT 失败时后端不落库，所以本地编辑保留着让人可以重试或改回去
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
      <div class="sub">按线路分组 · 保存后立即同步到 DNS 服务商</div>
      <RouterLink class="mini" to="/settings">DNS 服务商设置</RouterLink>
      <button class="mini" type="button" :disabled="!dirty" @click="load">放弃改动</button>
      <button class="primary" type="button" :disabled="!dirty || saving" @click="save">
        {{ saving ? '保存中…' : '保存并同步' }}
      </button>
    </header>

    <div v-if="loading && !data" class="hint">正在加载…</div>
    <div v-else-if="error" class="hint error">
      {{ error }}
      <button class="mini" type="button" @click="load">重试</button>
    </div>

    <div v-else-if="data" class="lines">
      <section v-for="l in data.lines" :key="l.code" class="line">
        <header class="line-head">
          <span class="code">{{ l.code }}</span>
          <span class="name">{{ l.name }}</span>
          <span class="count">{{ l.entries.filter((e) => e.dns_enabled).length }} /
            {{ l.entries.length }} 个节点参与解析</span>
        </header>

        <ul class="entries">
          <li v-for="e in l.entries" :key="e.node" class="entry" :class="{ off: !e.dns_enabled }">
            <span class="node">{{ e.node }}</span>
            <input
              class="w"
              type="text"
              inputmode="numeric"
              :value="weightOf(l.code, e.node, e.weight)"
              @input="setWeight(l.code, e.node, e.weight, ($event.target as HTMLInputElement).value)"
            />
            <span class="bar">
              <span
                class="fill"
                :style="{
                  width: `${localShare(l.code, l.entries).get(e.node) ?? 0}%`,
                  background: e.dns_enabled ? 'var(--accent)' : 'var(--border-strong)',
                }"
              />
            </span>
            <span class="share">{{ (localShare(l.code, l.entries).get(e.node) ?? 0).toFixed(1) }}%</span>
            <span class="st" :class="e.status">{{ e.status }}</span>
            <!--
              退出解析的节点必须说清「权重还在、但不承载流量」，
              否则人看到 weight 40 / share 0 会以为界面算错了。
            -->
            <span v-if="!e.dns_enabled" class="why">
              {{ e.status === 'down' ? '离线，已自动退出解析' : '已手动暂停解析' }}
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
.count {
  margin-left: auto;
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  color: var(--text-faint);
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
