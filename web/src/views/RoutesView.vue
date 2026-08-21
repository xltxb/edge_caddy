<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import NewRouteModal from '@/components/routes/NewRouteModal.vue'
import { useConfigStore } from '@/stores/config'

/**
 * 反代路由 —— **只读目录**。
 *
 * 编辑与删除都在工作台完成（PRD §5：工作台是全站唯一可写入口）。
 * 这一页的作用是「扫读」：一眼看出哪些域名回哪儿、处置方式是什么、
 * 谁还没下发到节点。
 */
const config = useConfigStore()
const router = useRouter()
const newOpen = ref(false)

onMounted(() => {
  if (!config.routes.length) void config.fetchAll().catch(() => {})
})

function edit(domain: string): void {
  void router.push({ name: 'workbench', params: { key: `route:${domain}` } })
}

const BLOCK_LABEL: Record<string, string> = {
  abort: '静默断连',
  '403': '返回 403',
  '404': '返回 404',
}
</script>

<template>
  <section class="panel">
    <header class="head">
      <div class="title">反代路由</div>
      <div class="sub">共 {{ config.routes.length }} 条 · 编辑与删除在配置工作台</div>
      <button class="primary" type="button" @click="newOpen = true">新建路由</button>
    </header>

    <div v-if="config.loading && !config.routes.length" class="hint">正在加载…</div>
    <div v-else-if="config.error" class="hint error">
      {{ config.error }}
      <button class="mini" type="button" @click="config.fetchAll()">重试</button>
    </div>
    <div v-else-if="!config.routes.length" class="hint">
      还没有反代路由。到配置工作台新建第一条。
    </div>

    <table v-else class="table">
      <thead>
        <tr>
          <th>域名</th>
          <th>回源地址</th>
          <th>处置方式</th>
          <th>回源 mTLS</th>
          <th>白名单</th>
          <th>请求体上限</th>
          <th>版本</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="r in config.routes" :key="r.domain">
          <td class="mono strong">{{ r.domain }}</td>
          <td class="mono">{{ r.upstream }}</td>
          <td>
            <span class="tag" :class="r.block_mode === 'abort' ? 'ok' : 'warn'">
              {{ BLOCK_LABEL[r.block_mode] }}
            </span>
          </td>
          <td>
            <!-- ADR-0008：这是回源时出示客户端证书，不是要求访问者出示 -->
            <span class="mono muted">{{ r.mtls ? '出示 edge-mtls' : '—' }}</span>
          </td>
          <td class="mono">{{ r.whitelist.length }} 条</td>
          <td class="mono muted">{{ r.body_max }}</td>
          <td>
            <span v-if="r.version === 0" class="tag new">尚未下发</span>
            <span v-else class="mono muted">v{{ r.version }}</span>
          </td>
          <td class="right">
            <button class="mini" type="button" @click="edit(r.domain)">在工作台编辑</button>
          </td>
        </tr>
      </tbody>
    </table>
  </section>

  <NewRouteModal v-if="newOpen" @close="newOpen = false" />
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
}
</style>
