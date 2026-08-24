<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { http, errorText } from '@/api/http'
import type { DnsTargetWire, SettingsUpdateWire, SettingsWire } from '@/api/types'
import type { SettingsPutBody } from '@/api/requests'
import { useUiStore } from '@/stores/ui'

/**
 * 解析域名 —— **主控要把解析记录写到哪几个主机名上**（契约 §11）。
 *
 * ## 为什么它不叫「域名」
 *
 * 这个系统里「域名」有两个意思，而它们是**两件不相干的事**：
 *
 * - 反代路由里的**站点域名** —— 访问者在浏览器里输的那个，决定请求命中哪条路由。
 * - 这一页的**解析落点** —— 主控把边缘节点的 IP 写进 DNS 时，写到哪个名字上。
 *
 * 两者常常是同一个字符串，所以更要在名字上分开：叫「域名」的话，
 * 人会以为在这里加一条就能让那个站点跑起来 —— 而路由那边一个字都没变。
 *
 * ## 它跟服务商配置的关系
 *
 * **凭证与 `kind` 只有一份**（同一个服务商账号），在系统设置里配；
 * 这一页只管「往哪儿写」。所以这里把 `kind` 显示成只读的一行 ——
 * 它决定了 `zone_id` 要不要填，人得看得见，但改它是另一页的事。
 *
 * ## 保存的是整份，不是增量
 *
 * `PUT` 给了 `targets` 就**整个替换**（契约 §11）。合并的话「删掉一个域名」
 * 没法表达：给一个少一条的列表会被读成「这几条不变」，而那个域名会永远留在
 * 配置里继续被同步。
 */
const ui = useUiStore()

const saved = ref<SettingsWire | null>(null)
const loading = ref(false)
const saving = ref(false)
const error = ref<string | null>(null)

/**
 * 本地编辑。`null` = 这次没碰过。
 *
 * 一旦碰了任何一行就整份拷出来 —— 见上面「保存的是整份」。
 */
const edit = ref<DnsTargetWire[] | null>(null)

async function load(): Promise<void> {
  /*
   * **本地编辑要在这里清掉。** 「放弃改动」直接调 load()，不清的话那些行会
   * 留下来盖住重新取回的值 —— 人按了「放弃」而屏幕没变。
   */
  edit.value = null
  loading.value = true
  error.value = null
  try {
    saved.value = await http.get<SettingsWire>('/settings')
  } catch (e) {
    error.value = errorText(e, '加载域名配置失败')
  } finally {
    loading.value = false
  }
}

onMounted(load)

/** 显示用：编辑过就用编辑中的，否则用服务端回显的。 */
const targets = computed<DnsTargetWire[]>(
  () => edit.value ?? saved.value?.dns_provider.targets ?? [],
)

/**
 * **碰过就算改动**，哪怕改回了原样。
 *
 * 比较内容看着更准，但它会漏掉一种真实意图：把一条删掉再加一条一模一样的 ——
 * 那两次操作之间人可能确实想让服务商那边重推一次。而多发一次整体替换的
 * 代价是零（它本来就是全量）。
 */
const dirty = computed(() => edit.value !== null)

/** 碰之前先整份拷出来 —— 之后所有增删改都在副本上。 */
function ensureCopy(): DnsTargetWire[] {
  if (!edit.value) {
    edit.value = (saved.value?.dns_provider.targets ?? []).map((t) => ({ ...t }))
  }
  return edit.value
}

function setField(i: number, key: keyof DnsTargetWire, v: string): void {
  const list = ensureCopy()
  if (list[i]) list[i][key] = v
}

function addRow(): void {
  ensureCopy().push({ domain: '', sub: '', zone_id: '' })
}

/**
 * 删一行。**最后一条也允许删** —— 那表达的是「不再管任何域名」，
 * 是个合法的意图；拦住它等于逼人去数据库里改。
 */
function removeRow(i: number): void {
  ensureCopy().splice(i, 1)
}

const kind = computed(() => saved.value?.dns_provider.kind ?? '')

const CF_KINDS = ['cloudflare', 'cloudflare_dns']
/**
 * **两个 Cloudflare 都要 `zone_id`，DNSPod 不要**（契约 §11）。
 *
 * 列在一处而不是散在各个 `v-if` 里 —— 加第三个 kind 时我正是漏改了那几处
 * `kind === 'cloudflare'`，于是纯 DNS 下那个框根本不渲染，而人填不了它。
 */
const isCloudflare = computed(() => CF_KINDS.includes(kind.value))

const KIND_LABEL: Record<string, string> = {
  dnspod: 'DNSPod',
  cloudflare: 'Cloudflare（负载均衡）',
  cloudflare_dns: 'Cloudflare（纯 DNS）',
}
const kindLabel = computed(() => KIND_LABEL[kind.value] ?? '')

async function save(): Promise<void> {
  if (!saved.value || !edit.value) return
  saving.value = true
  try {
    /*
     * **只发 `targets`，一个别的字段都不带。**
     *
     * 契约 §11 的规矩是「不给 = 不动」，所以少发是安全的；而**多发不是** ——
     * 这一页跟系统设置页现在能同时开着，把这一页加载时看到的 `kind`
     * 原样发回去，就会把另一页刚改的那个覆盖掉，**且没有任何一处会说出来**。
     *
     * 少发一个字段最坏是「这次没改到」，多发一个字段最坏是「悄悄改回去了」。
     * 两种错的代价不对称，所以往少了发。
     */
    const body: SettingsPutBody = { dns_provider: { targets: edit.value } }
    const r = await http.put<SettingsUpdateWire>('/settings', body)
    edit.value = null
    await load()
    /*
     * **「已保存」和「解析已推到服务商」是两件事**（契约 §11）。
     *
     * 改完域名主控会立刻推一次解析，而那一步可能没成 —— 没配服务商、
     * 凭证过期、服务商报错。库里那份已经改了，**而访问者还被送到旧地址**。
     * 只说「已保存」的话，人会以为改完就生效了。
     *
     * `detail` 原样显示，不自己编：它说的是这一次为什么是这个结果。
     */
    if (r?.dns_synced) {
      ui.toast('ok', '域名已保存，解析已推到服务商', r.detail)
    } else if (r?.detail) {
      ui.toast('warn', '域名已保存，但解析没推上去', r.detail)
    } else if (!kind.value) {
      /*
       * **没配服务商时主控回的 `detail` 是空串** —— 在真主控上试出来的。
       * 这一句不是编的：它说的是 `GET` 回来的事实（`kind` 是空的），
       * 不是替主控解释它没说的话。
       */
      ui.toast('warn', '域名已保存，但没有服务商可推', '先去系统设置选一家并填凭证')
    } else {
      // 配了服务商而主控没给原因 —— 说「不知道」，不替它编一个
      ui.toast('warn', '域名已保存，但解析没推上去', '主控没说明原因')
    }
  } catch (e) {
    ui.toast('warn', '保存失败', errorText(e, ''))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <section class="panel">
    <header class="head">
      <div class="title">解析域名</div>
      <div class="sub">主控把节点 IP 写到这几个名字上</div>
      <button class="mini" type="button" :disabled="!dirty" @click="load">放弃改动</button>
      <button class="primary" type="button" :disabled="!dirty || saving" @click="save">
        {{ saving ? '保存中…' : '保存' }}
      </button>
    </header>

    <div v-if="loading && !saved" class="hint">正在加载…</div>
    <div v-else-if="error" class="hint error">
      {{ error }}
      <button class="mini" type="button" @click="load">重试</button>
    </div>

    <div v-else-if="saved" class="groups">
      <section class="group">
        <!--
          **没选服务商时，这一页填什么都不会生效。**

          说在最前面而不是等保存后再说：域名存得进去（后端不拦），
          只是没有任何地方会去写那条记录 —— **一个存得下、看得见、
          而完全不起作用的配置**，比报错难发现得多。
        -->
        <div v-if="!kind" class="hint warn">
          还没选 DNS 服务商。这里填的域名存得进去，但<b>不会被推到任何地方</b> ——
          先去<RouterLink to="/settings">系统设置</RouterLink>选一家并填凭证。
        </div>

        <div class="row">
          <label>服务商</label>
          <div class="ctl">
            <span class="mono kind">{{ kindLabel || '（未选择）' }}</span>
            <!--
              只读，改它是系统设置那一页的事。**放在这里是因为它决定了下面要不要
              填 Zone ID** —— 人看不见它的话，那一列的出现与消失就成了没来由的。
            -->
            <RouterLink class="linkish" to="/settings">去系统设置更换</RouterLink>
          </div>
        </div>

        <div class="row">
          <label>要管的域名</label>
          <div class="ctl stack">
            <div v-if="!targets.length" class="empty-targets">
              还没有域名。加一条 —— 在这之前解析不会被推到任何地方。
            </div>
            <div v-for="(t, i) in targets" :key="i" class="target">
              <input
                :data-field="i === 0 ? 'domain' : undefined"
                :value="t.domain"
                class="text t-domain"
                placeholder="example.com"
                aria-label="解析域名"
                @input="setField(i, 'domain', ($event.target as HTMLInputElement).value)"
              />
              <input
                :value="t.sub"
                class="text t-sub"
                placeholder="子域（可空）"
                aria-label="子域前缀"
                @input="setField(i, 'sub', ($event.target as HTMLInputElement).value)"
              />
              <input
                v-if="isCloudflare"
                :data-field="i === 0 ? 'zone_id' : undefined"
                :value="t.zone_id"
                class="text mono t-zone"
                placeholder="Zone ID"
                aria-label="Zone ID"
                @input="setField(i, 'zone_id', ($event.target as HTMLInputElement).value)"
              />
              <button class="mini" type="button" title="删掉这一条" @click="removeRow(i)">
                删除
              </button>
              <span class="t-preview mono">{{ t.sub ? `${t.sub}.${t.domain}` : t.domain }}</span>
            </div>
            <button class="mini add" type="button" @click="addRow">添加域名</button>
            <p class="note">
              记录写在 <code>子域.域名</code> 上，子域留空则直接写在域名上。
              <b>所有域名共用同一组节点与权重</b> —— DNS 调度页那张表不分域名。
            </p>
            <p v-if="isCloudflare" class="note">
              <code>zone_id</code> 每条各填：同一个 zone 下的多个子域填同一个，
              不同顶级域各填各的。
            </p>
          </div>
        </div>
      </section>
    </div>
  </section>
</template>

<style scoped>
@import './catalog.css';
@import './settings.css';

.kind {
  color: var(--text-strong);
}
</style>
