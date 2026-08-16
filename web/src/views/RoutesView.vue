<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import StatusPill from '@/components/base/StatusPill.vue'
import { useRoutesStore } from '@/stores/routes'
import { validateRoute } from '@/utils/validators'
import type { Route } from '@/api/types'

const store = useRoutesStore()
const creating = ref(false)
const editing = ref('')
const saveErr = ref('')

const form = reactive({
  domain: '', upstream: '', block: 'abort' as Route['block'],
  mtls: false, compress: true, body_max: '5MB', wlText: '',
})

onMounted(() => store.load())

const wl = computed(() => form.wlText.split('\n').map((s) => s.trim()).filter(Boolean))
const errs = computed(() => validateRoute({ ...form, wl: wl.value }))
const canSubmit = computed(() => Object.keys(errs.value).length === 0)

function startCreate() {
  Object.assign(form, {
    domain: '', upstream: '', block: 'abort', mtls: false,
    compress: true, body_max: '5MB', wlText: '0.0.0.0/0',
  })
  creating.value = true
  editing.value = ''
  saveErr.value = ''
}

function startEdit(r: Route) {
  Object.assign(form, {
    domain: r.domain, upstream: r.upstream, block: r.block, mtls: r.mtls,
    compress: r.compress, body_max: r.body_max, wlText: (r.wl ?? []).join('\n'),
  })
  editing.value = r.domain
  creating.value = false
  saveErr.value = ''
}

function cancel() {
  creating.value = false
  editing.value = ''
}

async function submit() {
  if (!canSubmit.value) return
  saveErr.value = ''
  const body = {
    domain: form.domain, upstream: form.upstream, block: form.block,
    mtls: form.mtls, compress: form.compress, body_max: form.body_max, wl: wl.value,
  }
  try {
    if (editing.value) await store.update(editing.value, body)
    else await store.create(body)
    cancel()
  } catch (e) {
    saveErr.value = (e as Error).message
  }
}

async function remove(r: Route) {
  if (!window.confirm(`删除路由 ${r.domain}？\n\n删除后需要重新下发才会从节点上移除。`)) return
  try {
    await store.remove(r.domain)
  } catch (e) {
    saveErr.value = (e as Error).message
  }
}
</script>

<template>
  <div class="wrap">
    <section class="card head">
      <span>反代路由</span>
      <div class="acts">
        <button class="btn ghost" :disabled="store.deploying || !store.routes.length" @click="store.deploy()">
          {{ store.deploying ? '下发中…' : '校验并推送' }}
        </button>
        <button class="btn" @click="startCreate">新建路由</button>
      </div>
    </section>

    <!-- 逐节点结果，不做「整体成功/失败」的合并展示（PRD §7） -->
    <section v-if="store.lastDeploy" class="card pad">
      <div class="t">
        本次下发 <span class="mono">{{ store.lastDeploy.cfg_version }}</span>
      </div>
      <div v-for="row in store.lastDeploy.results" :key="row.node" class="drow">
        <span class="mono">{{ row.node }}</span>
        <StatusPill :tone="row.state === 'ok' ? 'ok' : 'danger'" :text="row.state === 'ok' ? '已生效' : '失败'" />
        <!-- 失败时展示 Caddy 的原文，不做归类：排查时唯一有用的就是那段原文 -->
        <span class="mono detail">{{ row.res }}</span>
      </div>
    </section>
    <section v-if="store.deployError" class="card pad err">{{ store.deployError }}</section>

    <section v-if="creating || editing" class="card pad form">
      <div class="ftitle">{{ creating ? '新建路由' : `编辑 ${editing}` }}</div>
      <label class="f">
        <span>域名</span>
        <input v-model.trim="form.domain" class="inp mono" :disabled="!creating"
               :class="{ bad: errs.domain }" placeholder="api.example.com" />
        <em v-if="errs.domain" class="e">{{ errs.domain }}</em>
      </label>
      <label class="f">
        <span>回源地址</span>
        <input v-model.trim="form.upstream" class="inp mono" :class="{ bad: errs.upstream }"
               placeholder="10.8.0.2:8080" />
        <em v-if="errs.upstream" class="e">{{ errs.upstream }}</em>
      </label>
      <label class="f narrow">
        <span>请求体上限</span>
        <input v-model.trim="form.body_max" class="inp mono" :class="{ bad: errs.body_max }" />
        <em v-if="errs.body_max" class="e">{{ errs.body_max }}</em>
        <em v-else class="hint">MB 是十进制 10⁶，MiB 才是 2²⁰</em>
      </label>
      <label class="f narrow">
        <span>非白名单处置</span>
        <select v-model="form.block" class="inp">
          <option value="abort">abort</option>
          <option value="403">403</option>
          <option value="404">404</option>
        </select>
        <em class="hint">
          {{ form.block === 'abort'
            ? 'abort 直接切断连接，扫描器嗅探不到应用存在'
            : `返回 ${form.block}，会暴露该域名后有服务在运行` }}
        </em>
      </label>
      <label class="f wide">
        <span>白名单（每行一条 IP / CIDR）</span>
        <textarea v-model="form.wlText" class="inp mono" rows="4" :class="{ bad: errs.wl }" />
        <em v-if="errs.wl" class="e">{{ errs.wl }}</em>
        <em v-else class="hint">只填 0.0.0.0/0 等于放行所有来源；留空同样不生成拒绝规则。</em>
      </label>
      <div class="f narrow">
        <span>回源 mTLS</span>
        <label class="sw"><input v-model="form.mtls" type="checkbox" /> 回源时出示客户端证书</label>
      </div>
      <div class="f narrow">
        <span>响应压缩</span>
        <label class="sw"><input v-model="form.compress" type="checkbox" /> zstd + gzip</label>
      </div>
      <p v-if="saveErr" class="err">{{ saveErr }}</p>
      <div class="wide">
        <button class="btn" :disabled="!canSubmit" @click="submit">保存</button>
        <button class="btn ghost" @click="cancel">取消</button>
      </div>
    </section>

    <section class="card">
      <div class="thead mono">
        <span>域名</span><span>回源</span><span>处置</span><span>白名单</span><span>版本</span><span></span>
      </div>
      <div v-if="store.loadError" class="pad err">{{ store.loadError }}</div>
      <div v-else-if="!store.routes.length" class="pad empty">还没有路由。点右上角「新建路由」。</div>
      <div v-for="r in store.routes" :key="r.domain" class="row">
        <span class="mono dom">{{ r.domain }}</span>
        <span class="mono sub">{{ r.upstream }}</span>
        <span class="mono sub">{{ r.block }}</span>
        <span class="mono sub">{{ (r.wl ?? []).length }} 条</span>
        <span class="mono sub">
          <StatusPill v-if="r.ver === 0" tone="warn" text="未下发" />
          <template v-else>v{{ r.ver }}</template>
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
.wrap { display: flex; flex-direction: column; gap: 14px; }
.card { background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; }
.head { display: flex; align-items: center; justify-content: space-between; padding: 12px 16px; font-weight: 600; color: var(--text-strong); }
.acts { display: flex; gap: 8px; }
.pad { padding: 14px 16px; }
.btn { padding: 6px 13px; border: 0; border-radius: 8px; cursor: pointer; background: var(--accent); color: var(--text-on-accent); font-size: 12.5px; font-weight: 600; }
.btn:hover:not(:disabled) { background: var(--accent-hover); }
.btn.ghost { background: var(--surface-sunken); color: var(--text-body); }
.t { font-size: 11.5px; color: var(--text-muted); margin-bottom: 8px; }
.drow { display: flex; align-items: center; gap: 10px; padding: 5px 0; font-size: 12px; }
.detail { color: var(--text-muted); font-size: 11.5px; }
.err { color: var(--danger-text); font-size: 13px; margin: 0; }
.empty { color: var(--text-muted); font-size: 13px; }

.form { display: flex; flex-wrap: wrap; gap: 14px; }
.ftitle { flex: 1 1 100%; font-size: 13.5px; font-weight: 700; color: var(--text-strong); }
.f { display: flex; flex-direction: column; gap: 5px; flex: 1 1 260px; }
.f.narrow { flex: 0 0 190px; }
.f.wide, .wide { flex: 1 1 100%; }
.f > span { font-size: 12px; font-weight: 600; color: var(--text-strong); }
.inp { padding: 7px 10px; border-radius: 8px; font-size: 13px; border: 1px solid var(--border-default); background: var(--surface-card); color: var(--text-strong); }
.inp.bad { border-color: var(--danger); }
.e { font-size: 11.5px; color: var(--danger-text); font-style: normal; }
.hint { font-size: 11px; color: var(--text-faint); font-style: normal; line-height: 1.5; }
.sw { display: flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--text-body); }

.thead, .row { display: grid; grid-template-columns: minmax(0,1.3fr) minmax(0,1fr) 70px 80px 90px 110px; gap: 12px; padding: 9px 16px; border-bottom: 1px solid var(--border-subtle); align-items: center; }
.thead { font-size: 10px; letter-spacing: .08em; text-transform: uppercase; color: var(--text-faint); font-weight: 600; }
.row:last-child { border-bottom: 0; }
.dom { font-size: 12.5px; font-weight: 600; color: var(--text-strong); }
.sub { font-size: 11.5px; color: var(--text-muted); }
.ops { display: flex; gap: 10px; justify-content: flex-end; }
.lnk { background: none; border: 0; cursor: pointer; font-size: 12px; color: var(--accent-text); padding: 0; }
.lnk.danger { color: var(--danger-text); }
</style>
