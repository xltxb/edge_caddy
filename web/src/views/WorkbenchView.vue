<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import StatusPill from '@/components/base/StatusPill.vue'
import { useDraftsStore, routeKey } from '@/stores/drafts'
import { useDeployStore } from '@/stores/deploys'
import { post } from '@/api/http'
import { diffLines, foldUnchanged } from '@/utils/diff'
import { readableConfig } from '@/utils/readable'
import { validateRoute } from '@/utils/validators'
import type { DeployResponse, Route } from '@/api/types'

const drafts = useDraftsStore()
const deploys = useDeployStore()
const route = useRoute()
const router = useRouter()
const sel = ref('')
const modal = ref<{ current: string; next: string; keys: string[] } | null>(null)
const modalErr = ref('')
const busy = ref(false)
const checked = ref<Record<string, boolean>>({})

/** keyFromURL 取出 URL 里的资源键。为空表示没指定。 */
const keyFromURL = computed(() => {
  const k = route.params.key
  return typeof k === 'string' ? k : ''
})

/**
 * 选中项跟着 URL 走。
 *
 * 不读这个参数的话，命令面板敲域名跳过来会落在列表第一条上——
 * 人以为自己打开了 shop，改的却是 api。
 */
function syncFromURL() {
  const k = keyFromURL.value
  if (k) {
    sel.value = k
    return
  }
  if (!sel.value && drafts.live.length) sel.value = routeKey(drafts.live[0].domain)
}

/** 指定了资源键却找不到对应路由——不能装作选中了第一条。 */
const missing = computed(() => keyFromURL.value !== '' && !drafts.liveOf(keyFromURL.value))

onMounted(async () => {
  await drafts.load()
  syncFromURL()
})

// URL 变了就换选中项：面板可以在工作台页面里再敲一次域名跳到另一条
watch(keyFromURL, syncFromURL)

/** pick 点树上的条目时同步 URL，让地址栏可分享、可后退。 */
function pick(domain: string) {
  void router.replace(`/workbench/${encodeURIComponent(routeKey(domain))}`)
}

// 离开且有未推送草稿时确认（前端文档 §3）
onBeforeRouteLeave(() => {
  if (!drafts.dirtyKeys.length) return true
  return window.confirm(`还有 ${drafts.totalChanges} 处改动没有推送，确定离开吗？`)
})

const cur = computed<Route | undefined>(() => drafts.effective(sel.value))
const base = computed<Route | undefined>(() => drafts.liveOf(sel.value))
const errs = computed(() => (cur.value ? validateRoute(cur.value) : {}))
const hasErrors = computed(() => Object.keys(errs.value).length > 0)

// 右栏是**可读表示**，不是将要下发的字节（ADR-0007）
const readableDiff = computed(() => {
  if (!cur.value || !base.value) return []
  return foldUnchanged(diffLines(readableConfig(base.value), readableConfig(cur.value)), 3)
})

function set<K extends keyof Route>(field: K, value: Route[K]) {
  drafts.setField(sel.value, field, value)
}

function wlText() {
  return (cur.value?.wl ?? []).join('\n')
}

async function openConfirm() {
  modalErr.value = ''
  busy.value = true
  try {
    const keys = drafts.dirtyKeys.filter((k) => checked.value[k])
    const r = await post<{ current: string; next: string }>('/config/preview', { res_keys: keys })
    modal.value = { ...r, keys }
  } catch (e) {
    modalErr.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

// 确认弹层里的 diff 是**后端权威渲染**的两侧，不是右栏那份可读表示
const authoritativeDiff = computed(() =>
  modal.value ? foldUnchanged(diffLines(modal.value.current, modal.value.next), 3) : [],
)

async function confirmPush() {
  if (!modal.value) return
  busy.value = true
  modalErr.value = ''
  try {
    const r = await post<DeployResponse>('/deploys', { res_keys: modal.value.keys })
    deploys.start(r.deploy_id, r.results.map((x) => x.node))
    for (const row of r.results) {
      deploys.applyFrame({
        type: 'deploy_progress',
        data: { deploy_id: r.deploy_id, node: row.node, state: row.state, res: row.res },
      })
    }
    modal.value = null
    await drafts.load()
  } catch (e) {
    modalErr.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

// 默认只勾当前操作人自己的改动（PRD §6.1 的变更摘要 + 我们定的「推送时勾选」）
function resetChecks(me: string) {
  const next: Record<string, boolean> = {}
  for (const k of drafts.dirtyKeys) next[k] = drafts.authorOf(k) === me || drafts.authorOf(k) === ''
  checked.value = next
}
defineExpose({ resetChecks })
</script>

<template>
  <div class="wb">
    <!-- 左：资源树 -->
    <aside class="col tree">
      <div class="ch">配置资源</div>
      <button v-for="r in drafts.live" :key="r.domain" class="ti"
              :class="{ on: sel === routeKey(r.domain) }" @click="pick(r.domain)">
        <span class="mono n">{{ r.domain }}</span>
        <span class="mono v">v{{ r.ver }}</span>
        <span v-if="drafts.isDirty(routeKey(r.domain))" class="dot" title="有未推送改动" />
      </button>
      <div v-if="!drafts.live.length" class="empty">还没有路由</div>
    </aside>

    <!-- 中：类型化表单 -->
    <section class="col form">
      <div v-if="missing" class="gone">
        找不到路由 <b class="mono">{{ keyFromURL.replace(/^route:/, '') }}</b>——它可能已被删除。
      </div>
      <div class="ch">
        <span class="mono" data-testid="current-domain">{{ cur?.domain ?? (missing ? keyFromURL.replace(/^route:/, '') : '—') }}</span>
        <StatusPill v-if="drafts.isDirty(sel)" tone="warn" :text="`${drafts.changeCount(sel)} 处未推送`" />
        <StatusPill v-else tone="muted" text="与线上一致" />
      </div>
      <div v-if="cur" class="fb">
        <label class="f"><span>回源地址</span>
          <input class="inp mono" :class="{ bad: errs.upstream }" :value="cur.upstream"
                 @input="set('upstream', ($event.target as HTMLInputElement).value)" />
          <em v-if="errs.upstream" class="e">{{ errs.upstream }}</em>
        </label>
        <label class="f"><span>请求体上限</span>
          <input class="inp mono" :class="{ bad: errs.body_max }" :value="cur.body_max"
                 @input="set('body_max', ($event.target as HTMLInputElement).value)" />
          <em v-if="errs.body_max" class="e">{{ errs.body_max }}</em>
        </label>
        <label class="f"><span>非白名单处置</span>
          <select class="inp" :value="cur.block"
                  @change="set('block', ($event.target as HTMLSelectElement).value as Route['block'])">
            <option value="abort">abort</option><option value="403">403</option><option value="404">404</option>
          </select>
        </label>
        <label class="f wide"><span>白名单（每行一条）</span>
          <textarea class="inp mono" rows="4" :class="{ bad: errs.wl }" :value="wlText()"
                    @input="set('wl', ($event.target as HTMLTextAreaElement).value.split('\n'))" />
          <em v-if="errs.wl" class="e">{{ errs.wl }}</em>
        </label>
        <div class="f"><span>响应压缩</span>
          <label class="sw"><input type="checkbox" :checked="cur.compress"
                 @change="set('compress', ($event.target as HTMLInputElement).checked)" /> zstd + gzip</label>
        </div>
        <div class="f"><span>回源 mTLS</span>
          <label class="sw"><input type="checkbox" :checked="cur.mtls"
                 @change="set('mtls', ($event.target as HTMLInputElement).checked)" /> 出示客户端证书</label>
        </div>
        <button v-if="drafts.isDirty(sel)" class="btn ghost" @click="drafts.revert(sel)">放弃本资源的改动</button>
      </div>
    </section>

    <!-- 右：可读表示 -->
    <section class="col diff">
      <div class="ch">
        <span>变更摘要</span>
        <!-- 措辞不能说成「将要下发的配置」——它不是（ADR-0007） -->
        <em class="note">可读表示，非下发字节</em>
      </div>
      <div class="dbody mono">
        <template v-for="(it, i) in readableDiff" :key="i">
          <div v-if="it.kind === 'fold'" class="fold">⋯ {{ it.count }} 行未变更</div>
          <div v-else class="dl" :class="it.kind">
            <span class="sign">{{ it.kind === 'add' ? '+' : it.kind === 'del' ? '-' : ' ' }}</span>{{ it.text }}
          </div>
        </template>
        <div v-if="!readableDiff.length" class="empty">选一个资源查看</div>
      </div>
    </section>

    <!-- 底栏 -->
    <footer class="bar">
      <div>
        <div class="bt">{{ drafts.dirtyKeys.length ? `${drafts.dirtyKeys.length} 个资源共 ${drafts.totalChanges} 处未推送` : '没有待推送的变更' }}</div>
        <div v-if="hasErrors" class="berr">当前资源有非法输入，无法推送</div>
      </div>
      <button class="btn" :disabled="!drafts.dirtyKeys.length || hasErrors || busy"
              @click="resetChecks('abiu'); openConfirm()">
        {{ busy ? '校验中…' : '校验并推送' }}
      </button>
    </footer>

    <!-- 确认弹层：真实 diff -->
    <div v-if="modal" class="mask" @click.self="modal = null">
      <div class="modal">
        <div class="mh">确认下发</div>
        <div class="mnote">以下是<b>后端权威渲染</b>的实际差异，与右栏的可读表示不同。</div>
        <div class="picks">
          <label v-for="k in drafts.dirtyKeys" :key="k" class="pick">
            <input type="checkbox" v-model="checked[k]" @change="openConfirm()" />
            <span class="mono">{{ k }}</span>
            <span class="who">{{ drafts.authorOf(k) || '—' }} · {{ drafts.changeCount(k) }} 处</span>
          </label>
        </div>
        <div class="dbody mono mscroll">
          <template v-for="(it, i) in authoritativeDiff" :key="i">
            <div v-if="it.kind === 'fold'" class="fold">⋯ {{ it.count }} 行未变更</div>
            <div v-else class="dl" :class="it.kind">
              <span class="sign">{{ it.kind === 'add' ? '+' : it.kind === 'del' ? '-' : ' ' }}</span>{{ it.text }}
            </div>
          </template>
        </div>
        <p v-if="modalErr" class="berr">{{ modalErr }}</p>
        <div class="mf">
          <button class="btn ghost" @click="modal = null">取消</button>
          <button class="btn" :disabled="busy || !modal.keys.length" @click="confirmPush">
            {{ busy ? '推送中…' : `推送选中的 ${modal.keys.length} 项` }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.wb { display: grid; grid-template-columns: 210px minmax(0,1fr) minmax(0,1fr); grid-template-rows: 1fr auto; gap: 12px; height: calc(100vh - 120px); }
.col { background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; overflow: auto; }
.ch { display: flex; align-items: center; gap: 9px; padding: 11px 14px; border-bottom: 1px solid var(--border-subtle); font-size: 12.5px; font-weight: 700; color: var(--text-strong); }
.note { font-size: 10.5px; color: var(--text-faint); font-style: normal; margin-left: auto; }
.ti { display: flex; align-items: center; gap: 7px; width: 100%; padding: 7px 12px; background: none; border: 0; cursor: pointer; text-align: left; color: var(--text-body); }
.ti.on { background: var(--accent-subtle); color: var(--accent-text); }
.ti .n { font-size: 12px; flex: 1; overflow: hidden; text-overflow: ellipsis; }
.ti .v { font-size: 10px; color: var(--text-faint); }
.dot { width: 6px; height: 6px; border-radius: 50%; background: var(--accent); flex: none; }
.gone { margin: 10px 12px 0; padding: 9px 12px; border-radius: 9px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font-size: 12px; color: var(--danger-text); }
.empty { padding: 14px; font-size: 12.5px; color: var(--text-muted); }
.fb { padding: 14px; display: flex; flex-wrap: wrap; gap: 12px; }
.f { display: flex; flex-direction: column; gap: 5px; flex: 1 1 200px; }
.f.wide { flex: 1 1 100%; }
.f > span { font-size: 11.5px; font-weight: 600; color: var(--text-strong); }
.inp { padding: 6px 9px; border-radius: 8px; font-size: 12.5px; border: 1px solid var(--border-default); background: var(--surface-card); color: var(--text-strong); }
.inp.bad { border-color: var(--danger); }
.e { font-size: 11px; color: var(--danger-text); font-style: normal; }
.sw { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text-body); }
.dbody { padding: 8px 0; font-size: 11.5px; line-height: 1.75; }
.dl { padding: 0 12px; white-space: pre-wrap; word-break: break-all; }
.dl .sign { display: inline-block; width: 12px; color: var(--text-faint); }
.dl.add { background: var(--success-subtle); color: var(--success-text); }
.dl.del { background: var(--danger-subtle); color: var(--danger-text); }
.fold { padding: 3px 12px; font-size: 10.5px; color: var(--text-faint); background: var(--surface-sunken); }
.bar { grid-column: 1 / -1; display: flex; align-items: center; justify-content: space-between; padding: 11px 16px; background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; }
.bt { font-size: 12.5px; font-weight: 600; color: var(--text-strong); }
.berr { font-size: 11.5px; color: var(--danger-text); margin: 3px 0 0; }
.btn { padding: 7px 14px; border: 0; border-radius: 8px; cursor: pointer; background: var(--accent); color: var(--text-on-accent); font-size: 12.5px; font-weight: 600; }
.btn.ghost { background: var(--surface-sunken); color: var(--text-body); }
.mask { position: fixed; inset: 0; background: var(--surface-overlay); display: grid; place-items: center; z-index: 20; }
.modal { width: min(720px, 92vw); max-height: 82vh; display: flex; flex-direction: column; background: var(--surface-card); border-radius: 16px; border: 1px solid var(--border-subtle); }
.mh { padding: 14px 18px; font-size: 14px; font-weight: 700; color: var(--text-strong); border-bottom: 1px solid var(--border-subtle); }
.mnote { padding: 10px 18px 0; font-size: 11.5px; color: var(--text-muted); }
.picks { padding: 10px 18px; display: flex; flex-direction: column; gap: 6px; border-bottom: 1px solid var(--border-subtle); }
.pick { display: flex; align-items: center; gap: 8px; font-size: 12px; color: var(--text-body); }
.who { margin-left: auto; font-size: 11px; color: var(--text-faint); }
.mscroll { overflow: auto; flex: 1; }
.mf { display: flex; justify-content: flex-end; gap: 8px; padding: 12px 18px; border-top: 1px solid var(--border-subtle); }
</style>
