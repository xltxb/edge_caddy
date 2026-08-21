<script setup lang="ts">
import { onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useConfigStore } from '@/stores/config'
import type { RuleWire } from '@/api/types'

/**
 * 访问控制 —— **只读目录**。启停、编辑、域名绑定都在工作台完成。
 *
 * 一条规则的 `apply_to` 为空数组时，它**不生效**。那是半成品状态，
 * 不是「对所有域名生效」（契约 §6.2）——这一页必须把它显示成未绑定，
 * 否则人会以为自己已经保护了全站。
 */
const config = useConfigStore()
const router = useRouter()

onMounted(() => {
  if (!config.rules.length) void config.fetchAll().catch(() => {})
})

const TYPE_LABEL: Record<string, string> = {
  ip_whitelist: 'IP 白名单',
  service_secret: '服务密钥',
  jwt_bearer: 'JWT Bearer',
}

function summary(r: RuleWire): string {
  const s = r.spec as Record<string, unknown>
  if (r.type === 'ip_whitelist') return `${(s.ips as string[] | undefined)?.length ?? 0} 条来源`
  if (r.type === 'service_secret') return `${String(s.header ?? '')} · ${String(s.algo ?? '')}`
  return String(s.iss ?? '')
}

function edit(id: string): void {
  void router.push({ name: 'workbench', params: { key: encodeURIComponent(`rule:${id}`) } })
}
</script>

<template>
  <section class="panel">
    <header class="head">
      <div class="title">访问控制</div>
      <div class="sub">共 {{ config.rules.length }} 条 · 启停与编辑在配置工作台</div>
    </header>

    <div v-if="config.loading && !config.rules.length" class="hint">正在加载…</div>
    <div v-else-if="config.error" class="hint error">
      {{ config.error }}
      <button class="mini" type="button" @click="config.fetchAll()">重试</button>
    </div>
    <div v-else-if="!config.rules.length" class="hint">还没有访问规则。</div>

    <table v-else class="table">
      <thead>
        <tr>
          <th>规则</th>
          <th>类型</th>
          <th>要点</th>
          <th>状态</th>
          <th>应用到</th>
          <th>版本</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="r in config.rules" :key="r.id">
          <td>
            <div class="strong">{{ r.name }}</div>
            <div class="mono muted small">{{ r.id }}</div>
          </td>
          <td class="mono">{{ TYPE_LABEL[r.type] }}</td>
          <td class="mono muted">{{ summary(r) }}</td>
          <td>
            <span class="tag" :class="r.enabled ? 'ok' : 'warn'">
              {{ r.enabled ? '生效中' : '已停用' }}
            </span>
          </td>
          <td>
            <!-- 空绑定必须显示成「未绑定」，不能留白让人以为是全局生效 -->
            <span v-if="!r.apply_to.length" class="tag warn">未绑定域名，不生效</span>
            <span v-else class="mono muted">{{ r.apply_to.join('、') }}</span>
          </td>
          <td class="mono muted">v{{ r.version }}</td>
          <td class="right">
            <button class="mini" type="button" @click="edit(r.id)">在工作台编辑</button>
          </td>
        </tr>
      </tbody>
    </table>
  </section>
</template>

<style scoped>
@import './catalog.css';
.small {
  font-size: var(--fs-micro);
}
</style>
