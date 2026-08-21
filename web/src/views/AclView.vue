<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useConfigStore } from '@/stores/config'
import { errorText } from '@/api/http'
import type { RuleWire } from '@/api/types'

/**
 * 访问控制 —— 目录页。启停、编辑、域名绑定都在工作台完成。
 *
 * **唯一在这一页写的是共享密钥**，因为它不能走草稿：草稿存在主控上、由
 * `GET /drafts` 全局回显（契约 §6.4），密钥进草稿就等于被回显。它走
 * `PUT /rules/:id` 的顶层 `secret`，直写立即生效、不排队等下发 —— 放进工作台
 * 会让人以为它也在等下发，那是个会误事的错觉。
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

/**
 * 状态列说的是**这条规则此刻做不做事**，不是那个开关的位置。
 *
 * 只看 enabled 的话，一条启用了但没绑任何域名的规则会显示「生效中」，而右边的
 * 「应用到」同时说它不生效 —— 两列各说各话，人只会信左边那个绿的。
 *
 * 「不做事」的三种原因都收在这里，而不是各开一列：多一列就多一次自相矛盾的机会。
 * 密钥那一条在真主控上进不来（`PUT /rules/:id` 不给密钥就被 1002 拒），
 * 但这个布尔是后端真发的字段 —— 它要是 false，说「生效中」就是撒谎。
 */
function statusOf(r: RuleWire): { text: string; tone: 'ok' | 'warn' } {
  if (!r.enabled) return { text: '已停用', tone: 'warn' }
  if (!r.apply_to.length) return { text: '已启用，未生效', tone: 'warn' }
  if (r.type === 'service_secret' && r.spec.secret_configured === false) {
    return { text: '未设置密钥', tone: 'warn' }
  }
  return { text: '生效中', tone: 'ok' }
}

/* ── 删除规则 ── */

const removing = ref<string | null>(null)
const removeBusy = ref(false)
const removeError = ref<string | null>(null)

const removeTarget = computed(() =>
  removing.value ? config.rules.find((r) => r.id === removing.value) : undefined,
)

async function confirmRemove(): Promise<void> {
  const id = removing.value
  if (!id) return
  removeBusy.value = true
  removeError.value = null
  try {
    await config.deleteRule(id)
    removing.value = null
  } catch (e) {
    removeError.value = errorText(e, '删除失败')
  } finally {
    removeBusy.value = false
  }
}

/* ── 设置共享密钥（直写，不进草稿）── */

const editingSecret = ref<string | null>(null)
const secretInput = ref('')
const saving = ref(false)
const secretError = ref<string | null>(null)

function openSecret(id: string): void {
  editingSecret.value = id
  secretInput.value = ''
  secretError.value = null
}

async function saveSecret(id: string): Promise<void> {
  // 留空即不改（契约 §6.2）。这里干脆不发请求：发一个什么也不改的 PUT 会写下
  // 一条审计，读起来像「有人换过密钥」，而其实没有。
  if (!secretInput.value) {
    editingSecret.value = null
    return
  }
  saving.value = true
  secretError.value = null
  try {
    await config.setRuleSecret(id, secretInput.value)
    editingSecret.value = null
    secretInput.value = ''
  } catch (e) {
    secretError.value = errorText(e, '保存失败')
  } finally {
    saving.value = false
  }
}

function edit(id: string): void {
  void router.push({ name: 'workbench', params: { key: `rule:${id}` } })
}
</script>

<template>
  <section class="panel">
    <header class="head">
      <div class="title">访问控制</div>
      <div class="sub">共 {{ config.rules.length }} 条 · 启停与编辑在配置工作台</div>
    </header>

    <!--
      「双轨」不是分类学趣味：两条轨道的失败方式不同 —— IP 白名单挡的是来源，
      签名与 JWT 挡的是身份。只说「三种规则」的话，人不会意识到自己可能
      只保护了其中一维。
    -->
    <p class="lead">
      双轨准入：第三方系统走 <b>IP 白名单 + 请求签名</b>，终端客户端走 <b>JWT</b>。
      后两者由边缘节点上的 Agent 验签，Caddy 通过 forward_auth 委托给它。
    </p>

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
        <template v-for="r in config.rules" :key="r.id">
          <tr>
          <td>
            <div class="strong">{{ r.name }}</div>
            <div class="mono muted small">{{ r.id }}</div>
          </td>
          <td class="mono">{{ TYPE_LABEL[r.type] }}</td>
          <td class="mono muted">{{ summary(r) }}</td>
          <td>
            <span class="tag" :class="statusOf(r).tone">{{ statusOf(r).text }}</span>
          </td>
          <td>
            <!-- 空绑定必须显示成「未绑定」，不能留白让人以为是全局生效 -->
            <span v-if="!r.apply_to.length" class="tag warn">未绑定域名</span>
            <span v-else class="mono muted">{{ r.apply_to.join('、') }}</span>
          </td>
          <td class="mono muted">v{{ r.version }}</td>
          <td class="right">
            <button
              v-if="r.type === 'service_secret'"
              class="mini"
              type="button"
              @click="openSecret(r.id)"
            >
              更换密钥
            </button>
            <button class="mini" type="button" @click="edit(r.id)">在工作台编辑</button>
            <button class="mini danger" type="button" @click="removing = r.id">删除</button>
          </td>
          </tr>

          <tr v-if="editingSecret === r.id" class="secret-row">
            <td colspan="7">
              <form class="secret-form" @submit.prevent="saveSecret(r.id)">
                <label class="secret-label" :for="`secret-${r.id}`">共享密钥</label>
                <input
                  :id="`secret-${r.id}`"
                  v-model="secretInput"
                  class="secret-input"
                  type="password"
                  autocomplete="new-password"
                  placeholder="留空并保存不会改动密钥"
                />
                <button class="mini primary" type="submit" :disabled="saving">
                  {{ saving ? '保存中…' : '保存' }}
                </button>
                <button class="mini" type="button" @click="editingSecret = null">取消</button>
                <p class="secret-note">
                  只写入不回显，任何接口都取不回原值。<b>直写立即生效</b>，不进草稿、不等下发。
                </p>
                <p v-if="secretError" class="secret-err">{{ secretError }}</p>
              </form>
            </td>
          </tr>
        </template>
      </tbody>
    </table>
  </section>

  <!--
    删除要确认，而确认框要说清**那个时间差**：规则从控制台消失，但节点上那条
    还在拦，直到下一次下发。方向是 fail-closed（拦多了不是漏了），所以不算险，
    但不说的话，人删完会以为立刻不拦了，然后去试一个「应该能过」的请求。
  -->
  <div v-if="removeTarget" class="mask" @click.self="removing = null">
    <div class="modal" role="dialog" aria-modal="true" aria-label="删除访问规则">
      <div class="modal-title">删除 {{ removeTarget.name }}</div>
      <p class="modal-note">
        <b class="mono">{{ removeTarget.id }}</b> 会从控制台移除，它的未下发草稿一并清掉。
      </p>
      <p v-if="removeTarget.apply_to.length" class="modal-note warn">
        这条规则当前绑在 {{ removeTarget.apply_to.join('、') }} 上。<b>删除不会立刻停止拦截</b>
        —— 节点上那份配置里它还在，直到下一次下发把新配置推下去。
      </p>
      <p v-else class="modal-note">这条规则没有绑定域名，本来就不生效。</p>
      <p v-if="removeError" class="secret-err">{{ removeError }}</p>
      <div class="modal-actions">
        <button class="mini" type="button" @click="removing = null">取消</button>
        <button class="mini danger" type="button" :disabled="removeBusy" @click="confirmRemove">
          {{ removeBusy ? '删除中…' : '确认删除' }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
@import './catalog.css';
.mask {
  position: fixed;
  inset: 0;
  background: var(--surface-overlay);
  z-index: var(--z-modal);
  display: grid;
  place-items: center;
  padding: var(--space-6);
}
.modal {
  width: min(460px, 100%);
  background: var(--surface-raised);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-xl);
  padding: var(--space-5);
}
.modal-title {
  font-family: var(--font-mono);
  font-size: var(--fs-base);
  font-weight: var(--weight-bold);
  color: var(--text-strong);
  margin-bottom: var(--space-3);
}
.modal-note {
  margin: 0 0 var(--space-2-5, 10px);
  font-size: var(--fs-2xs);
  color: var(--text-muted);
  line-height: 1.7;
}
.modal-note.warn {
  color: var(--warning-text);
}
.modal-note b {
  color: var(--text-body);
  font-weight: var(--weight-semibold);
}
.modal-note.warn b {
  color: var(--warning-text);
}
.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-2);
  margin-top: var(--space-4);
}
.secret-row td {
  background: var(--surface-sunken, var(--bg-subtle));
}
.secret-form {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
}
.secret-label {
  font-size: var(--fs-2xs);
  color: var(--text-body);
  font-weight: var(--weight-medium);
}
.secret-input {
  width: 260px;
  padding: 5px 8px;
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-sm);
  background: var(--surface);
  color: var(--text-body);
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
}
.secret-note {
  margin: 0;
  flex-basis: 100%;
  font-size: var(--fs-micro);
  color: var(--text-muted);
  line-height: 1.6;
}
.secret-note b {
  color: var(--warning-text);
  font-weight: var(--weight-semibold);
}
.secret-err {
  margin: 0;
  flex-basis: 100%;
  font-size: var(--fs-micro);
  color: var(--danger-text);
}
.small {
  font-size: var(--fs-micro);
}
.lead {
  margin: 0;
  padding: 10px var(--space-4);
  border-bottom: 1px solid var(--border-subtle);
  font-size: var(--fs-2xs);
  color: var(--text-muted);
  line-height: 1.7;
}
.lead b {
  color: var(--text-body);
  font-weight: var(--weight-semibold);
}
</style>
