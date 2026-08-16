<script setup lang="ts">
import { onMounted } from 'vue'
import { useNodesStore } from '@/stores/nodes'
const nodes = useNodesStore()
onMounted(() => nodes.load())
</script>

<template>
  <div class="wrap">
    <section class="card">
      <div class="kpis">
        <div class="kpi">
          <div class="k">在线节点</div>
          <div class="v mono">{{ nodes.onlineCount }} / {{ nodes.nodes.length }}</div>
        </div>
        <div class="kpi">
          <div class="k">配置漂移</div>
          <div class="v mono">{{ nodes.driftedCount }}</div>
          <!-- 漂移只比对版本号，不检查节点上的配置内容（ADR-0002）。
               不写清楚的话，读者会理所当然地以为它能发现篡改。 -->
          <div class="note">只比对版本号，不检查节点上的配置内容</div>
        </div>
        <div class="kpi">
          <div class="k">当前基线</div>
          <div class="v mono sm">{{ nodes.baseline || '尚未发布' }}</div>
        </div>
      </div>
    </section>
    <section class="card pad">
      <p class="todo">连接数、回源率与事件流属于工单 #9，尚未实现。</p>
    </section>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 14px; }
.card { background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; }
.pad { padding: 18px; }
.kpis { display: grid; grid-template-columns: repeat(3, 1fr); }
.kpi { padding: 18px 20px; border-right: 1px solid var(--border-subtle); }
.kpi:last-child { border-right: 0; }
.k { font-size: 10.5px; letter-spacing: .08em; text-transform: uppercase; color: var(--text-faint); font-weight: 600; }
.v { font-size: 26px; font-weight: 700; color: var(--text-strong); margin-top: 6px; font-variant-numeric: tabular-nums; }
.v.sm { font-size: 15px; }
.note { font-size: 11px; color: var(--text-faint); margin-top: 4px; line-height: 1.5; }
.todo { margin: 0; font-size: 13px; color: var(--text-muted); }
</style>
