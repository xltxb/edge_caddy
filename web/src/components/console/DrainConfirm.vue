<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useNodesStore } from '@/stores/nodes'

/**
 * 下线确认。全局挂载（App.vue 里与命令面板并排），因为行内按钮和命令面板
 * 都要经过它——放进节点页的话，在别的页面敲 drain 就没人拦。
 */
const nodes = useNodesStore()
const reason = ref('')
const node = computed(() => nodes.pendingDrain)

watch(node, () => (reason.value = ''))
</script>

<template>
  <div v-if="node" class="mask" data-testid="drain-confirm">
    <div class="box" role="alertdialog" :aria-label="`确认下线 ${node}`">
      <div class="ttl">确认下线 {{ node }}？</div>
      <!-- 说清楚会发生什么，而不只是「确定吗？」 -->
      <p class="say">
        该节点将被标记为下线，<strong>不再承接</strong>新的配置下发；已经在它上面跑的
        Caddy 不会被停掉，流量要靠 DNS 调度摘除。恢复需要节点重新上报心跳。
      </p>
      <input v-model="reason" class="rz" placeholder="下线原因（可选，会写进审计日志）" aria-label="下线原因" />
      <div class="acts">
        <button class="btn ghost" data-action="cancel" @click="nodes.cancelDrain()">取消</button>
        <button class="btn danger" data-action="confirm" @click="nodes.confirmDrain(reason)">确认下线</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mask { position: fixed; inset: 0; background: rgba(0, 0, 0, .38); display: grid; place-items: center; z-index: 60; }
.box { width: min(430px, 92vw); background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; padding: 18px; }
.ttl { font-size: 14.5px; font-weight: 700; color: var(--text-strong); margin-bottom: 8px; }
.say { margin: 0 0 12px; font-size: 12.5px; line-height: 1.65; color: var(--text-body); }
.rz { width: 100%; box-sizing: border-box; padding: 7px 10px; border: 1px solid var(--border-subtle); border-radius: 8px; background: var(--surface-sunken); color: var(--text-strong); font-size: 12.5px; }
.acts { display: flex; justify-content: flex-end; gap: 8px; margin-top: 14px; }
.btn { padding: 6px 14px; border-radius: 8px; border: 1px solid var(--border-subtle); cursor: pointer; font-size: 12.5px; font-weight: 600; }
.ghost { background: transparent; color: var(--text-body); }
.danger { background: var(--danger-text); border-color: var(--danger-text); color: #fff; }
</style>
