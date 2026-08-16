<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useDnsStore } from '@/stores/dns'
import { useRoutesStore } from '@/stores/routes'

const d = useDnsStore()
const routes = useRoutesStore()
const picked = ref('')

onMounted(async () => {
  await routes.load()
  if (!picked.value && routes.routes.length) picked.value = routes.routes[0].domain
})

watch(picked, (v) => {
  if (v) void d.load(v)
})

const SYNC_TEXT: Record<string, string> = {
  synced: '线上解析与库里一致',
  drifted: '线上解析与库里不一致',
  unknown: '读不到线上解析，无法判断是否一致',
}
</script>

<template>
  <div class="wrap">
    <section class="card head">
      <div class="left">
        <span>DNS 调度</span>
        <select v-model="picked" class="sel" aria-label="域名">
          <option v-for="r in routes.routes" :key="r.domain" :value="r.domain">{{ r.domain }}</option>
        </select>
      </div>
      <div class="acts">
        <button class="btn ghost" :disabled="d.saving" @click="d.save(false)">仅保存</button>
        <button class="btn" :disabled="d.saving" @click="d.save(true)">
          {{ d.saving ? '下发中…' : '保存并下发' }}
        </button>
      </div>
    </section>

    <!-- 三态，不是两态：「读不到线上」和「已同步」必须分开显示 -->
    <section class="card pad sync" :class="d.syncState" data-testid="sync-state">
      <b>{{ SYNC_TEXT[d.syncState] }}</b>
      <span v-if="d.syncState === 'drifted'" class="detail">{{ d.driftSummary }}</span>
      <span v-else-if="d.syncState === 'unknown'" class="detail">{{ d.liveError }}</span>
    </section>

    <section v-if="d.saveError" class="card pad err" data-testid="save-error">{{ d.saveError }}</section>
    <section v-if="d.loadError" class="card pad err">{{ d.loadError }}</section>

    <section v-for="line in d.lines" :key="line" class="card">
      <div class="lh">{{ line }}</div>
      <div v-if="!d.rowsOf(line).length" class="pad muted">这条线路上还没有配置节点</div>
      <div v-for="row in d.rowsOf(line)" :key="row.node" class="row" :data-row="`${line}:${row.node}`">
        <span class="mono nid">{{ row.node }}</span>
        <span class="mut">{{ row.city }}</span>
        <span class="mono mut">{{ row.ip || '—' }}</span>
        <span class="st" :class="{ off: row.status === 'down' }">
          {{ row.status === 'down' ? '离线 · 已退出解析' : '在线' }}
        </span>
        <input
          class="w"
          type="number"
          min="0"
          :value="row.weight"
          :aria-label="`${row.node} 在 ${line} 的权重`"
          @input="d.setWeight(line, row.node, Number(($event.target as HTMLInputElement).value))"
        />
        <!-- 占比只算在线且权重非零的：离线节点已退出解析，计入分母会让人
             以为流量只走了一半 -->
        <span class="share" :data-share="`${line}:${row.node}`">{{ d.shareOf(line, row.node) }}%</span>
      </div>
    </section>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 14px; }
.card { background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; }
.head { display: flex; align-items: center; justify-content: space-between; padding: 12px 16px; font-weight: 600; color: var(--text-strong); }
.left { display: flex; align-items: center; gap: 12px; }
.acts { display: flex; gap: 8px; }
.sel { padding: 5px 9px; border: 1px solid var(--border-subtle); border-radius: 8px; background: var(--surface-sunken); color: var(--text-strong); font-size: 12.5px; }
.pad { padding: 14px 16px; }
.err { color: var(--danger-text); font-size: 13px; }
.muted { color: var(--text-muted); font-size: 13px; }
.btn { padding: 6px 13px; border: 0; border-radius: 8px; cursor: pointer; background: var(--accent); color: var(--text-on-accent); font-size: 12.5px; font-weight: 600; }
.btn.ghost { background: transparent; border: 1px solid var(--border-subtle); color: var(--text-body); }
.sync { display: flex; flex-direction: column; gap: 4px; font-size: 12.5px; }
.sync.synced b { color: var(--ok-text, #2e7d32); }
.sync.drifted b { color: var(--warn-text, #b26a00); }
.sync.unknown b { color: var(--danger-text); }
.sync .detail { color: var(--text-muted); font-size: 11.5px; }
.lh { padding: 10px 16px; font-size: 10px; letter-spacing: .08em; text-transform: uppercase; color: var(--text-faint); font-weight: 600; border-bottom: 1px solid var(--border-subtle); }
.row { display: grid; grid-template-columns: minmax(0,1.2fr) 90px 130px 150px 90px 60px; gap: 12px; padding: 9px 16px; border-bottom: 1px solid var(--border-subtle); align-items: center; }
.row:last-child { border-bottom: 0; }
.nid { font-size: 12.5px; font-weight: 600; color: var(--text-strong); }
.mut { font-size: 11.5px; color: var(--text-muted); }
.st { font-size: 11.5px; color: var(--text-muted); }
.st.off { color: var(--danger-text); }
.w { width: 78px; padding: 4px 8px; border: 1px solid var(--border-subtle); border-radius: 7px; background: var(--surface-sunken); color: var(--text-strong); font-size: 12.5px; }
.share { font-size: 12.5px; font-weight: 600; color: var(--text-strong); text-align: right; }
</style>
