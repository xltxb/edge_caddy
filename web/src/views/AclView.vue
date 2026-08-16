<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import StatusPill from '@/components/base/StatusPill.vue'
import { get, put, del } from '@/api/http'

interface RuleSpec {
  ips?: string[]; header?: string; ttl?: number
  issuer?: string; audience?: string; jwks?: string; skew?: number
  secret_set?: boolean
}
interface Rule {
  id: string; name: string; type: string; enabled: boolean
  spec: RuleSpec; apply_to: string[]; effective: boolean
}

const rules = ref<Rule[]>([])
const domains = ref<string[]>([])
const err = ref('')
const editing = ref<string | null>(null)

const form = reactive({
  id: '', name: '', type: 'jwt_bearer', enabled: true,
  ips: '', header: 'X-Service-Secret', secret: '', ttl: 300,
  issuer: '', audience: '', jwks: '', skew: 60,
  apply_to: [] as string[],
})

async function load() {
  err.value = ''
  try {
    rules.value = (await get<{ rules: Rule[] }>('/rules')).rules ?? []
    domains.value = ((await get<{ routes: { domain: string }[] }>('/routes')).routes ?? []).map((r) => r.domain)
  } catch (e) {
    err.value = (e as Error).message
  }
}
onMounted(load)

function startNew() {
  Object.assign(form, {
    id: '', name: '', type: 'jwt_bearer', enabled: true,
    ips: '', header: 'X-Service-Secret', secret: '', ttl: 300,
    issuer: '', audience: '', jwks: '', skew: 60, apply_to: [],
  })
  editing.value = ''
}

function startEdit(r: Rule) {
  Object.assign(form, {
    id: r.id, name: r.name, type: r.type, enabled: r.enabled,
    ips: (r.spec.ips ?? []).join('\n'),
    header: r.spec.header ?? 'X-Service-Secret',
    secret: '', // 密钥不回显，留空表示不改
    ttl: r.spec.ttl ?? 300,
    issuer: r.spec.issuer ?? '', audience: r.spec.audience ?? '',
    jwks: r.spec.jwks ?? '', skew: r.spec.skew ?? 60,
    apply_to: [...r.apply_to],
  })
  editing.value = r.id
}

const canSave = computed(() => {
  if (!form.id.trim()) return false
  if (form.type === 'jwt_bearer') return !!form.jwks.trim()
  if (form.type === 'service_secret') return !!form.header.trim() && (!!form.secret.trim() || !!editing.value)
  return form.ips.split('\n').some((s) => s.trim())
})

async function save() {
  if (!canSave.value) return
  err.value = ''
  const spec: RuleSpec & { secret?: string } = {}
  if (form.type === 'ip_whitelist') spec.ips = form.ips.split('\n').map((s) => s.trim()).filter(Boolean)
  else if (form.type === 'service_secret') {
    spec.header = form.header; spec.ttl = form.ttl
    if (form.secret.trim()) spec.secret = form.secret
  } else {
    spec.issuer = form.issuer; spec.audience = form.audience
    spec.jwks = form.jwks; spec.skew = form.skew
  }
  try {
    await put(`/rules/${encodeURIComponent(form.id.trim())}`, {
      name: form.name, type: form.type, enabled: form.enabled, spec, apply_to: form.apply_to,
    })
    editing.value = null
    await load()
  } catch (e) {
    err.value = (e as Error).message
  }
}

async function remove(r: Rule) {
  if (!window.confirm(`删除规则 ${r.id}？\n\n删除后需要重新下发才会从节点上移除。`)) return
  try { await del(`/rules/${encodeURIComponent(r.id)}`); await load() } catch (e) { err.value = (e as Error).message }
}

function toggleDomain(d: string) {
  const i = form.apply_to.indexOf(d)
  if (i >= 0) form.apply_to.splice(i, 1); else form.apply_to.push(d)
}

const typeLabel: Record<string, string> = {
  ip_whitelist: 'IP 白名单', service_secret: '服务密钥', jwt_bearer: 'JWT',
}
</script>

<template>
  <div class="wrap">
    <section class="card head">
      <span>访问控制</span>
      <button class="btn" @click="startNew">新建规则</button>
    </section>
    <section class="card pad note">
      多条规则之间是<b>「或」</b>：满足任意一条即放行（PRD 的双轨准入）。
      给一个域名<b>加</b>规则是在<b>增加</b>一条进入的路径，不是在收紧——想收紧应改已有规则。
    </section>
    <section v-if="err" class="card pad e">{{ err }}</section>

    <section v-if="editing !== null" class="card pad form">
      <label class="f"><span>规则 ID</span>
        <input v-model.trim="form.id" class="inp mono" :disabled="!!editing" placeholder="app-jwt" /></label>
      <label class="f"><span>名称</span><input v-model.trim="form.name" class="inp" /></label>
      <label class="f narrow"><span>类型</span>
        <select v-model="form.type" class="inp" :disabled="!!editing">
          <option value="jwt_bearer">JWT</option>
          <option value="service_secret">服务密钥</option>
          <option value="ip_whitelist">IP 白名单</option>
        </select></label>

      <template v-if="form.type === 'jwt_bearer'">
        <label class="f"><span>签发者 iss</span><input v-model.trim="form.issuer" class="inp mono" /></label>
        <label class="f"><span>受众 aud</span><input v-model.trim="form.audience" class="inp mono" /></label>
        <label class="f wide"><span>JWKS 地址</span>
          <input v-model.trim="form.jwks" class="inp mono" placeholder="https://idp.example.com/.well-known/jwks.json" />
          <em class="hint">边缘会真正验签（签名 / iss / aud / exp），并把已验证的主体透传给源站。</em></label>
      </template>
      <template v-else-if="form.type === 'service_secret'">
        <label class="f"><span>请求头</span><input v-model.trim="form.header" class="inp mono" /></label>
        <label class="f"><span>密钥</span>
          <input v-model="form.secret" class="inp mono" type="password"
                 :placeholder="editing ? '留空表示不修改' : ''" />
          <em class="hint">只写入不回显。签名含时间戳，超出重放窗口的请求会被拒。</em></label>
        <label class="f narrow"><span>重放窗口（秒）</span>
          <input v-model.number="form.ttl" class="inp mono" type="number" min="1" /></label>
      </template>
      <template v-else>
        <label class="f wide"><span>允许的 IP / CIDR（每行一条）</span>
          <textarea v-model="form.ips" class="inp mono" rows="4" /></label>
      </template>

      <div class="f wide"><span>应用到的域名</span>
        <div v-if="domains.length" class="chips">
          <button v-for="d in domains" :key="d" class="chip" :class="{ on: form.apply_to.includes(d) }"
                  @click="toggleDomain(d)">{{ d }}</button>
        </div>
        <em v-else class="hint">还没有路由。规则可以先建，等有了域名再绑。</em>
        <em v-if="!form.apply_to.length" class="hint warn">未绑定域名的规则不会生效。</em>
      </div>
      <div class="wide">
        <button class="btn" :disabled="!canSave" @click="save">保存</button>
        <button class="btn ghost" @click="editing = null">取消</button>
      </div>
    </section>

    <section class="card">
      <div class="thead mono"><span>规则</span><span>类型</span><span>绑定域名</span><span>状态</span><span></span></div>
      <div v-if="!rules.length" class="pad empty">还没有访问规则。</div>
      <div v-for="r in rules" :key="r.id" class="row">
        <span><span class="mono id">{{ r.id }}</span><span class="nm">{{ r.name }}</span></span>
        <span class="mono sub">{{ typeLabel[r.type] ?? r.type }}</span>
        <span class="mono sub">{{ r.apply_to.length ? r.apply_to.join('、') : '—' }}</span>
        <span>
          <StatusPill v-if="!r.enabled" tone="muted" text="已停用" />
          <StatusPill v-else-if="!r.effective" tone="warn" text="未绑定域名，不生效" />
          <StatusPill v-else tone="ok" text="生效中" />
        </span>
        <span class="ops">
          <button class="lnk" @click="startEdit(r)">编辑</button>
          <button class="lnk danger" @click="remove(r)">删除</button>
        </span>
      </div>
    </section>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 12px; }
.card { background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; }
.head { display: flex; align-items: center; justify-content: space-between; padding: 12px 16px; font-weight: 600; color: var(--text-strong); }
.pad { padding: 13px 16px; }
.note { font-size: 12px; color: var(--text-muted); line-height: 1.7; }
.e { color: var(--danger-text); font-size: 13px; }
.empty { color: var(--text-muted); font-size: 13px; }
.btn { padding: 6px 13px; border: 0; border-radius: 8px; cursor: pointer; background: var(--accent); color: var(--text-on-accent); font-size: 12.5px; font-weight: 600; }
.btn.ghost { background: var(--surface-sunken); color: var(--text-body); }
.form { display: flex; flex-wrap: wrap; gap: 12px; }
.f { display: flex; flex-direction: column; gap: 5px; flex: 1 1 220px; }
.f.narrow { flex: 0 0 170px; } .f.wide, .wide { flex: 1 1 100%; }
.f > span { font-size: 11.5px; font-weight: 600; color: var(--text-strong); }
.inp { padding: 7px 10px; border-radius: 8px; font-size: 12.5px; border: 1px solid var(--border-default); background: var(--surface-card); color: var(--text-strong); }
.hint { font-size: 11px; color: var(--text-faint); font-style: normal; line-height: 1.55; }
.hint.warn { color: var(--warning-text); }
.chips { display: flex; flex-wrap: wrap; gap: 6px; }
.chip { padding: 4px 10px; border-radius: 999px; border: 1px solid var(--border-default); background: none; cursor: pointer; font-size: 11.5px; color: var(--text-body); }
.chip.on { background: var(--accent-subtle); border-color: var(--accent-subtle-border); color: var(--accent-text); }
.thead, .row { display: grid; grid-template-columns: minmax(0,1.2fr) 90px minmax(0,1.2fr) 160px 110px; gap: 12px; padding: 9px 16px; border-bottom: 1px solid var(--border-subtle); align-items: center; }
.thead { font-size: 10px; letter-spacing: .08em; text-transform: uppercase; color: var(--text-faint); font-weight: 600; }
.row:last-child { border-bottom: 0; }
.id { font-size: 12.5px; font-weight: 600; color: var(--text-strong); }
.nm { font-size: 11px; color: var(--text-faint); margin-left: 7px; }
.sub { font-size: 11.5px; color: var(--text-muted); }
.ops { display: flex; gap: 10px; justify-content: flex-end; }
.lnk { background: none; border: 0; cursor: pointer; font-size: 12px; color: var(--accent-text); padding: 0; }
.lnk.danger { color: var(--danger-text); }
</style>
