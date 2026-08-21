<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { http, errorText } from '@/api/http'
import type { CertRenewWire, CertWire, Paged } from '@/api/types'
import { useUiStore } from '@/stores/ui'

/**
 * 证书。
 *
 * 「N / M 个节点」两列真相：N = Agent 回执里真正加载了这张证书的节点数，
 * M = 主控签发记录上应覆盖的节点数。**证书清单是回执，不是账本**
 * （CONTEXT.md）。N < M 意味着「下发到了但没生效」——这类故障在只显示
 * 一个数字的界面里是完全隐形的。
 */
const ui = useUiStore()
const items = ref<CertWire[]>([])
/** 正在续期的域名。续期是异步的，结果经 WS 事件回报，所以这里只表示「已受理」。 */
const renewing = ref<Set<string>>(new Set())
const checkingAll = ref(false)
const loading = ref(false)
const error = ref<string | null>(null)
const openRow = ref<string | null>(null)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    items.value = (await http.get<Paged<CertWire>>('/certs')).items
  } catch (e) {
    error.value = errorText(e, '加载证书失败')
  } finally {
    loading.value = false
  }
}

onMounted(load)

/**
 * 单张续期。
 *
 * **主控自己去 ACME 续，再随下一次下发把新证书内联带下去**（契约 §9），
 * 不是让节点去续 —— 边缘节点跑官方 Caddy，不持有 DNS 凭据（ADR-0001）。
 * 异步：接口立即返回，结果经 WS 事件回报。所以按钮回到可点状态不代表续好了。
 */
async function renew(domain: string): Promise<void> {
  renewing.value = new Set(renewing.value).add(domain)
  try {
    const r = await http.post<CertRenewWire>(`/certs/${encodeURIComponent(domain)}/renew`)
    ui.toast(
      r.accepted ? 'info' : 'warn',
      r.accepted ? `已受理 ${domain} 的续期` : `${domain} 的续期未被受理`,
      r.accepted ? '主控正在向 ACME 申请，完成后会出现在事件流里' : '',
    )
  } catch (e) {
    ui.toast('warn', '续期失败', errorText(e, ''))
  } finally {
    const next = new Set(renewing.value)
    next.delete(domain)
    renewing.value = next
  }
}

async function renewCheck(): Promise<void> {
  checkingAll.value = true
  try {
    await http.post('/certs/renew-check')
    ui.toast('info', '已发起全部证书的到期检查', '需要续期的会自动申请，结果见事件流')
  } catch (e) {
    ui.toast('warn', '检查失败', errorText(e, ''))
  } finally {
    checkingAll.value = false
  }
}

/** 到期条三档：≤7 天危、≤14 天警、其余正常。 */
function level(days: number): 'crit' | 'warn' | 'ok' {
  return days <= 7 ? 'crit' : days <= 14 ? 'warn' : 'ok'
}

const COLOR = { crit: 'var(--danger)', warn: 'var(--warning)', ok: 'var(--success)' }
const TEXT = { crit: 'var(--danger-text)', warn: 'var(--warning-text)', ok: 'var(--text-strong)' }

const expiring = computed(() => items.value.filter((c) => c.days_left <= 14))
const manual = computed(() => expiring.value.filter((c) => !c.auto_renew))
const mismatched = computed(() => items.value.filter((c) => c.loaded_nodes < c.expected_nodes))
</script>

<template>
  <section class="panel">
    <header class="head">
      <div class="title">证书</div>
      <div class="sub">共 {{ items.length }} 张 · 由主控集中签发（DNS-01）</div>
      <button class="mini" type="button" :disabled="checkingAll" @click="renewCheck">
        {{ checkingAll ? '检查中…' : '全部续期检查' }}
      </button>
    </header>

    <div v-if="expiring.length" class="banner warn">
      {{ expiring.length }} 张证书将在 14 天内到期<span v-if="manual.length">，其中
        {{ manual.length }} 张未开启自动续期，需要手动处理</span>。
    </div>
    <div v-if="mismatched.length" class="banner danger">
      {{ mismatched.length }} 张证书的节点回执少于签发记录——已下发但未在全部节点上生效。
    </div>

    <div v-if="loading && !items.length" class="hint">正在加载…</div>
    <div v-else-if="error" class="hint error">
      {{ error }}
      <button class="mini" type="button" @click="load">重试</button>
    </div>
    <!--
      空不等于坏。证书跟着路由走 —— 第一次下发之前这里本来就是空的，
      而一个只有表头的空表格什么也没说，看起来像加载失败。
    -->
    <div v-else-if="!items.length" class="hint">
      还没有证书。证书由主控在下发时自动为路由域名签发（DNS-01），
      所以第一次下发之前这里是空的。
      <RouterLink class="mini" to="/routes">去看反代路由</RouterLink>
    </div>

    <table v-else class="table">
      <thead>
        <tr>
          <th>域名</th>
          <th>签发者</th>
          <th>密钥</th>
          <th>剩余有效期</th>
          <th>续期</th>
          <th>节点（回执 / 签发）</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <template v-for="c in items" :key="c.domain">
          <tr>
            <td>
              <div class="mono strong">{{ c.domain }}</div>
              <div class="mono muted small">{{ c.scope }} · {{ c.challenge }}</div>
            </td>
            <td class="muted">{{ c.issuer }}</td>
            <td class="mono muted">{{ c.key_type }}</td>
            <td>
              <div class="bar">
                <span
                  class="fill"
                  :style="{
                    width: `${Math.min(100, (c.days_left / 90) * 100).toFixed(0)}%`,
                    background: COLOR[level(c.days_left)],
                  }"
                />
              </div>
              <div class="mono days" :style="{ color: TEXT[level(c.days_left)] }">
                {{ c.days_left }} 天
              </div>
            </td>
            <td>
              <span class="tag" :class="c.auto_renew ? 'ok' : 'warn'">
                {{ c.auto_renew ? '自动' : '手动' }}
              </span>
            </td>
            <td>
              <!-- 相等时中性；回执少于账面时转警示并可展开看缺哪几个 -->
              <button
                v-if="c.expected_nodes === 0"
                class="mono muted plain"
                type="button"
                disabled
              >
                仅主控
              </button>
              <button
                v-else-if="c.loaded_nodes < c.expected_nodes"
                class="mismatch"
                type="button"
                @click="openRow = openRow === c.domain ? null : c.domain"
              >
                ⚠ {{ c.loaded_nodes }} / {{ c.expected_nodes }} 个节点
              </button>
              <span v-else class="mono muted">{{ c.loaded_nodes }} / {{ c.expected_nodes }} 个节点</span>
            </td>
            <td class="right">
              <button
                class="mini"
                type="button"
                :disabled="renewing.has(c.domain)"
                @click="renew(c.domain)"
              >
                {{ renewing.has(c.domain) ? '受理中…' : '立即续期' }}
              </button>
            </td>
          </tr>
          <tr v-if="openRow === c.domain" class="detail-row">
            <td colspan="7">
              <div class="detail">
                <b>未加载该证书的节点：</b>
                <span class="mono">{{ c.missing_nodes.join('、') }}</span>
                <p class="note">
                  签发记录显示应覆盖 {{ c.expected_nodes }} 个节点，但只有
                  {{ c.loaded_nodes }} 个节点的 Agent 回报已加载。证书清单是<b>回执</b>，
                  不是账本——两者不一致意味着下发到了但没生效。
                </p>
              </div>
            </td>
          </tr>
        </template>
      </tbody>
    </table>
  </section>
</template>

<style scoped>
@import './catalog.css';
.head .sub {
  margin-right: auto;
}
.small {
  font-size: var(--fs-micro);
}
.banner {
  padding: 9px var(--space-4);
  font-size: var(--fs-xs);
  border-bottom: 1px solid var(--border-subtle);
}
.banner.warn {
  background: var(--warning-subtle);
  color: var(--warning-text);
}
.banner.danger {
  background: var(--danger-subtle);
  color: var(--danger-text);
}
.bar {
  width: 96px;
  height: 4px;
  border-radius: var(--radius-full);
  background: var(--surface-sunken);
  overflow: hidden;
}
.fill {
  display: block;
  height: 100%;
  border-radius: var(--radius-full);
}
.days {
  font-size: var(--fs-micro);
  margin-top: 3px;
}
.mismatch {
  padding: 2px 9px;
  border: 1px solid var(--warning);
  border-radius: var(--radius-full);
  background: var(--warning-subtle);
  color: var(--warning-text);
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  font-weight: var(--weight-semibold);
  cursor: pointer;
}
.plain {
  border: 0;
  background: transparent;
  font-size: var(--fs-xs);
  cursor: default;
}
.detail-row td {
  background: var(--surface-sunken);
}
.detail {
  font-size: var(--fs-xs);
  color: var(--text-body);
}
.note {
  margin: var(--space-1-5) 0 0;
  font-size: var(--fs-2xs);
  color: var(--text-muted);
  line-height: 1.6;
}
</style>
