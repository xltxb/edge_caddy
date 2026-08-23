<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { http, errorText } from '@/api/http'
import type { DnsProviderFields, SettingsWire } from '@/api/types'
import type { SettingsPutBody } from '@/api/requests'
import { useUiStore } from '@/stores/ui'

/**
 * 系统设置。
 *
 * 两条契约要求直接决定了这一页的形状：
 * 1. `master_endpoint` **必须是域名不是 IP**（后端会拒，code 1001）——本地先拦一次。
 * 2. **凭证只写入不回显**：响应里永远没有明文，只有 `configured: true/false`。
 *    所以这里不做「显示当前凭证」，只做「已配置 / 更换」。
 */
const ui = useUiStore()

const saved = ref<SettingsWire | null>(null)
const form = ref<SettingsWire | null>(null)
const loading = ref(false)
const saving = ref(false)
const error = ref<string | null>(null)
/** 更换凭证时才出现的明文输入框。空 = 不改动。 */

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    saved.value = await http.get<SettingsWire>('/settings')
    form.value = JSON.parse(JSON.stringify(saved.value)) as SettingsWire
  } catch (e) {
    error.value = errorText(e, '加载设置失败')
  } finally {
    loading.value = false
  }
}

onMounted(load)

const dirty = computed(
  () =>
    !!form.value &&
    !!saved.value &&
    (JSON.stringify(form.value) !== JSON.stringify(saved.value) || dnsDirty.value),
)

/** 「节点最长 N 秒后被摘除」—— 前端算，不需要后端给（契约 §11）。 */
const dropAfter = computed(() =>
  form.value ? form.value.heartbeat_interval_s * form.value.offline_threshold_count : 0,
)



/**
 * DNS 服务商这一块的字段分两类，**分界线是 `GET` 回不回显它**（契约 §11）。
 *
 * - **回显的**：`kind` / `domain` / `sub` / `credential_mode`。直接绑
 *   `form.dns_provider`，像普通表单那样双向编辑，脏值由整体 JSON 比对得出。
 * - **不回显的**：`zone_id` / `credential` / `email` / `account_id`。它们只有
 *   「这次要不要改」这一种状态，留在 `dnsEdit` 里，空 = 不改动。
 *
 * 这两类原先被混作一类，全塞进 `dnsEdit` 走「留空 = 不改动」。代价是**界面把
 * 它明明知道的事装成不知道**：已配的域名只能当灰占位符摆着，人分不清那是
 * 现值还是例子；更糟的是保存后 `dnsEdit` 清空，`undefined` 匹配不上任何
 * `<option>`，服务商下拉直接变空白 —— 看上去像刚把配置删了。
 *
 * 一个字段回不回显是后端的事；**它能不能显示是界面的事，不该跟着一起丢**。
 */
const dnsEdit = ref<DnsProviderFields>({})

/** 只有不回显的那几个才需要「填了才发」。回显字段的脏值走 `dirty` 的整体比对。 */
const dnsDirty = computed(() => Object.values(dnsEdit.value).some((v) => v !== undefined && v !== ''))

/** 凭证的三种状态。见模板里凭证那一行的注释。 */
const credTag = computed(() => {
  const d = form.value?.dns_provider
  if (!d?.configured) return { cls: 'warn', text: '未配置' }
  if (!d.kind) return { cls: 'warn', text: '有一份凭证，但没选服务商 —— 它现在没在用' }
  return { cls: 'ok', text: '已配置' }
})

/** Cloudflare 的两种凭证方式要的字段不同（契约 §11）。 */
const cfMode = computed(() => form.value?.dns_provider.credential_mode ?? '')
const kindNow = computed(() => form.value?.dns_provider.kind ?? '')

async function save(): Promise<void> {
  if (!form.value) return
  saving.value = true
  try {
    /*
     * **白名单：契约 §11 说 `PUT` 收哪几个，就只发哪几个。**
     *
     * `GET` 回来的那份里混着只读的状态位（`master_endpoint`、
     * `ops_bot_token_configured`、`warn_*`）。原先是 `{ ...form.value }` 再减掉
     * 几个 —— 那是个**黑名单**，要随后端加字段一起维护，漏一个就发出去一个。
     *
     * 这一段踩过两脚，都被后端这一轮加的 `DisallowUnknownFields` 照出来了：
     *
     * 1. 凭证发成了**顶层**的 `dns_credential`，而它嵌在 `dns_provider` 里。
     *    后端此前静默忽略并回 `code: 0` —— 界面弹「设置已保存」而什么都没存进去。
     *    **报错会让人再试，假象让人走开。**
     * 2. `ops_bot_token_configured` 是只读状态位，而我一直原样发回去。
     *
     * `master_endpoint` 也不发：它只读，`PUT` 带它一律 1002 —— 那个地址进了主控
     * 服务端证书的 SAN，运行时改不了。
     */
    const body: SettingsPutBody = {
      heartbeat_interval_s: form.value.heartbeat_interval_s,
      offline_threshold_count: form.value.offline_threshold_count,
      auto_drop_dns: form.value.auto_drop_dns,
    }
    /*
     * 契约 §11：**不带 `dns_provider` = 不动它；带一个空对象会把已配的清掉。**
     * 所以只在真有改动时才带上，并且带就带全 —— 回显的四个字段是当前界面上的
     * 值（没改就等于原值，发过去是幂等的），不回显的几个只在填了时才加。
     */
    const cur = form.value.dns_provider
    const savedDns = saved.value?.dns_provider
    const echoedChanged =
      !!savedDns &&
      (cur.kind !== savedDns.kind ||
        cur.domain !== savedDns.domain ||
        cur.sub !== savedDns.sub ||
        cur.credential_mode !== savedDns.credential_mode)

    if (echoedChanged || dnsDirty.value) {
      /*
       * 空串照发 —— 它是把已配的服务商清掉的唯一办法。
       *
       * 这里原先拦着不发，理由写的是「空串发过去只会换来一个 1001」，而那句
       * 我没验过：真主控收下了，`code: 0`，清空生效。拦住它等于把「取消配置」
       * 这条路堵死，还堵得悄无声息。
       */
      const p: DnsProviderFields = {
        kind: cur.kind as DnsProviderFields['kind'],
        domain: cur.domain,
        sub: cur.sub,
        credential_mode: cur.credential_mode as DnsProviderFields['credential_mode'],
      }
      for (const [k, v] of Object.entries(dnsEdit.value)) {
        if (v !== undefined && v !== '') (p as Record<string, unknown>)[k] = v
      }
      body.dns_provider = p
    }
    /*
     * **`PUT /settings` 回的是 `data: null`，不是新的设置对象。**
     *
     * 这里原先把返回值直接赋给 `form` —— 于是保存成功之后整页变空白，
     * 而 toast 说「设置已保存」。它确实保存了，只是界面把自己清掉了：
     * **一个正确的操作配一个坏掉的回显**，比操作失败更让人摸不着头脑，
     * 因为人会以为自己刚把配置弄没了。
     *
     * 保存后重新 GET 一次。多一趟请求，换掉一整类「回显与真相不一致」。
     */
    await http.put('/settings', body)
    dnsEdit.value = {}
    await load()
    ui.toast('ok', '设置已保存')
  } catch (e) {
    ui.toast('warn', '保存失败', errorText(e, ''))
  } finally {
    saving.value = false
  }
}

const clearing = ref(false)
const askClear = ref(false)

/**
 * 清掉整份 DNS 服务商配置，**凭证一起删**。
 *
 * 单独一个动作，不跟保存混在一起，因为它和保存做的是两件事：保存是「把我填的
 * 写进去」，清除是「把里面的拿出来」。契约 §11 也不许它俩同给 —— `clear` 与
 * 任何其他字段并存返回 `1002`，后端不肯在「先清再设」和「清掉一切」两种读法里
 * 替人挑一种。
 *
 * 这是**唯一**能删掉凭证的路径：逐字段填空清不掉它（空串 = 不改动，那是凭证不
 * 回显换来的语义），而一份再也用不到、也删不掉的凭证仍然是一把有效的 API Token。
 */
async function clearProvider(): Promise<void> {
  clearing.value = true
  try {
    await http.put('/settings', { dns_provider: { clear: true } })
    dnsEdit.value = {}
    await load()
    askClear.value = false
    ui.toast('ok', '已清除 DNS 服务商配置')
  } catch (e) {
    ui.toast('warn', '清除失败', errorText(e, ''))
  } finally {
    clearing.value = false
  }
}
</script>

<template>
  <section class="panel">
    <header class="head">
      <div class="title">系统设置</div>
      <div class="sub">凭证只写入不回显</div>
      <button class="mini" type="button" :disabled="!dirty" @click="load">放弃改动</button>
      <button
        class="primary"
        type="button"
        :disabled="!dirty || saving"
        @click="save"
      >
        {{ saving ? '保存中…' : '保存' }}
      </button>
    </header>

    <div v-if="loading && !form" class="hint">正在加载…</div>
    <div v-else-if="error" class="hint error">
      {{ error }}
      <button class="mini" type="button" @click="load">重试</button>
    </div>

    <div v-else-if="form" class="groups">
      <section class="group">
        <div class="group-title">主控接入</div>
        <!--
          **只读。** 它此前是个输入框，而那是错的两次：
          改它不会改变任何东西（拼安装命令用的是 EC_ADVERTISE），而它为空时
          我把「主控还没给值」渲染成了「你填错了」—— 那个红框还禁用了整个保存
          按钮，于是人配 DNS 服务商时被一个跟 DNS 毫无关系的字段挡死。
        -->
        <div class="row">
          <label>Agent 连接地址</label>
          <div class="ctl">
            <span class="mono">{{ form.master_endpoint || '（主控未公布）' }}</span>
            <p class="note">
              Agent 主动外连这个地址，由主控启动时的 <code>EC_ADVERTISE</code> 决定，
              <b>运行时改不了</b>：这个地址进了主控服务端证书的 SAN，而证书是启动时签的
              —— 改设置改不了证书，那时节点会连上一个证书里没有它的地址，握手直接失败。
              要换就改启动配置再重启主控。
            </p>
          </div>
        </div>
      </section>

      <section class="group">
        <div class="group-title">探活与自愈</div>
        <div class="row">
          <label>心跳间隔（秒）</label>
          <div class="ctl">
            <input v-model.number="form.heartbeat_interval_s" class="text narrow" type="text" />
          </div>
        </div>
        <div class="row">
          <label>连续超时次数</label>
          <div class="ctl">
            <input v-model.number="form.offline_threshold_count" class="text narrow" type="text" />
            <!-- 联动提示：两个数字单看都没意义，乘起来才是人关心的那件事 -->
            <p class="note strong">
              节点最长 <b>{{ dropAfter }}</b> 秒后被判定离线。
            </p>
          </div>
        </div>
        <div class="row">
          <label>自动摘除解析</label>
          <div class="ctl">
            <label class="switch">
              <input v-model="form.auto_drop_dns" type="checkbox" />
              <span>{{ form.auto_drop_dns ? '判定离线后自动退出 DNS 解析' : '判定离线后仍保留解析权重' }}</span>
            </label>
            <p v-if="!form.auto_drop_dns" class="note warn">
              关闭后，离线节点仍会分到流量，直到有人手动暂停它。
            </p>
          </div>
        </div>
      </section>

      <!--
        这一块原先只能改「凭证」，服务商 / 域名 / 凭证方式全是只读文本 ——
        而 kind 为空时那一行是**一片空白**。灰度环境上人的反馈是「找不到配置
        DNS 服务商的地方」，他分不出是没做还是没找到，而这两件事他的下一步
        完全不同（一个是等我们做，一个是再找找）。

        又一次「没有」表现成「我没找到」，这次是人类撞上的。
      -->
      <section class="group">
        <div class="group-title">DNS 服务商</div>

        <p v-if="!form.dns_provider.kind" class="group-lead warn">
          还没配。证书签发（DNS-01）、解析权重下发、节点下线时摘解析，
          三件事都要等这里填上。
        </p>

        <div class="row">
          <label for="dns-kind">服务商</label>
          <div class="ctl">
            <select id="dns-kind" v-model="form.dns_provider.kind" class="text sel">
              <option value="">（未选择）</option>
              <option value="cloudflare">Cloudflare</option>
              <option value="dnspod">DNSPod</option>
            </select>
            <p class="note">
              两家能表达的东西不一样：Cloudflare 的 DNS 记录没有线路概念，
              电信 / 联通 / 移动会被合并成「中国」。选定之后 DNS 调度页会按它的
              能力合并输入框。
            </p>
          </div>
        </div>

        <div class="row">
          <label for="dns-domain">解析域名</label>
          <div class="ctl">
            <input
              id="dns-domain"
              v-model="form.dns_provider.domain"
              class="text"
              placeholder="example.com"
            />
            <p class="note">记录写在这个域名下。</p>
          </div>
        </div>

        <div class="row">
          <label for="dns-sub">子域前缀</label>
          <div class="ctl">
            <input
              id="dns-sub"
              v-model="form.dns_provider.sub"
              class="text"
              placeholder="（可空）"
            />
            <p class="note">留空则记录直接写在解析域名上。</p>
          </div>
        </div>

        <!-- Cloudflare 两种凭证方式要的字段不同（契约 §11） -->
        <div v-if="kindNow === 'cloudflare'" class="row">
          <label for="dns-mode">凭证方式</label>
          <div class="ctl">
            <select id="dns-mode" v-model="form.dns_provider.credential_mode" class="text sel">
              <option value="">（未选择）</option>
              <option value="api_token">API Token（推荐，权限可收窄到单个 zone）</option>
              <option value="global_key">Global API Key</option>
            </select>
          </div>
        </div>

        <!--
          **Zone ID 与 Account ID 两种凭证方式都必填**（契约 §11）。

          `account_id` 此前被关在 `global_key` 分支里，而且占位符写着「可空」——
          于是 **api_token 模式下界面上根本没有这个输入框**，人填不了它。
          灰度上的症状：只填 kind / domain / token 保存成功、设置页显示已配置，
          而推权重时 Cloudflare 回
          `GET /accounts//load_balancers/pools` → 7003「Could not route to ...」。

          **那个双斜杠就是空字段**，而 Cloudflare 的错误消息不认识我们的字段名，
          它把人送去查 token 和权限 —— 离真因最远的两个地方。

          两个都是拼进 URL 的路径段：加权调度用的 pool 是账号级的，
          load balancer 挂在 zone 上。少任何一个都不是「功能弱一点」，
          是**整条推送不成立**。

          契约那张表当时把 `account_id` 写成「可选」，这个框是照它做的 ——
          **契约里的一句错会被忠实地复制出去**。
        -->
        <div v-if="kindNow === 'cloudflare'" class="row">
          <label for="dns-zone">Zone ID</label>
          <div class="ctl">
            <input
              id="dns-zone"
              v-model="dnsEdit.zone_id"
              class="text mono"
              :placeholder="form.dns_provider.configured ? '留空 = 不改动' : '必填'"
            />
          </div>
        </div>

        <div v-if="kindNow === 'cloudflare'" class="row">
          <label for="dns-account">Account ID</label>
          <div class="ctl">
            <input
              id="dns-account"
              v-model="dnsEdit.account_id"
              class="text mono"
              :placeholder="form.dns_provider.configured ? '留空 = 不改动' : '必填'"
            />
            <p class="note">
              加权调度的 pool 是账号级的 —— 少了它，推权重时 Cloudflare 会回
              「路由不到」，而那句话不会提到这个字段。
            </p>
          </div>
        </div>

        <!-- email 只有 Global Key 模式要（契约 §11），它才是真的按模式分的那个 -->
        <template v-if="kindNow === 'cloudflare' && cfMode === 'global_key'">
          <div class="row">
            <label for="dns-email">账号邮箱</label>
            <div class="ctl">
              <input
                id="dns-email"
                v-model="dnsEdit.email"
                class="text"
                :placeholder="form.dns_provider.configured ? '留空 = 不改动' : 'Global Key 模式必填'"
              />
            </div>
          </div>
        </template>

        <div class="row">
          <label for="dns-cred">凭证</label>
          <div class="ctl">
            <!--
              凭证与服务商是两笔账，可以对不上：清掉 `kind` 之后 `configured`
              照旧是 true —— 主控没有「删除凭证」这条路（`PUT` 带空串 = 不改动，
              那是凭证不回显换来的语义）。

              这三种状态原先被压成两种，于是横幅说「还没配」、徽标同时说
              「已配置」，两句话都在页面上，人没法判断哪句是真的。
              **说不清楚好过说错**。
            -->
            <span class="tag" :class="credTag.cls">{{ credTag.text }}</span>
            <input
              id="dns-cred"
              v-model="dnsEdit.credential"
              class="text"
              type="password"
              autocomplete="off"
              :placeholder="form.dns_provider.configured ? '留空 = 不改动现有凭证' : '粘贴凭证'"
            />
            <!--
              凭证只写入不回显（契约 §11）。这不是懒得做「查看」——
              回显一次就等于给了它一条泄露路径，而它能改写整个 zone。
            -->
            <p class="note">
              {{
                kindNow === 'dnspod'
                  ? 'DNSPod 的形式是「ID,Token」，中间一个逗号。'
                  : 'Cloudflare 填 API Token 或 Global API Key。'
              }}
              只写入不回显，换新的会让旧凭证立即失效。
            </p>
          </div>
        </div>

        <!--

          清除是**独立的动作**，所以它在这里，不在页头的「保存」旁边。

          两者做的事相反：保存是把我填的写进去，清除是把里面的拿出来。

          混在一起的话，一次「只想改域名」的保存有机会顺手删掉凭证。

        -->

        <div v-if="form.dns_provider.kind || form.dns_provider.configured" class="row">

          <label>清除配置</label>

          <div class="ctl">

            <button class="linkish" type="button" @click="askClear = true">

              清除 DNS 服务商配置

            </button>

            <p class="note">连凭证一起删。这是唯一能删掉凭证的路径。</p>

          </div>

        </div>


        <div class="row">
          <label>ops-bot Token</label>
          <div class="ctl">
            <span class="tag" :class="form.ops_bot_token_configured ? 'ok' : 'warn'">
              {{ form.ops_bot_token_configured ? '已配置' : '未配置' }}
            </span>
            <p class="note">自动化账号用它走 Bearer 鉴权，与人的会话 Cookie 分开。</p>
          </div>
        </div>
      </section>
    </div>

    <!--
      确认层。**说清楚会没掉什么，不问「你确定吗」。**

      三条链路会一起停：证书签发与续期（走 DNS-01，ADR-0001 定的主控集中签发）、
      解析权重下发、节点下线时摘解析。已经签发的证书还在机器上，到期前照常能用，
      但**续不了期** —— 这句要说，因为它的后果不在今天，人不会自己想到。
    -->
    <div v-if="askClear" class="mask" @click.self="askClear = false">
      <div class="modal" role="dialog" aria-modal="true" aria-label="清除 DNS 服务商配置">
        <div class="title">清除 DNS 服务商配置</div>
        <ul class="willgo">
          <li>服务商、解析域名、子域前缀、凭证方式全部清空</li>
          <li><b>凭证一并删除</b> —— 删了就没有了，主控不保留副本</li>
        </ul>
        <p class="note">
          之后这三件事会停：证书签发与续期（走 DNS-01）、解析权重下发、
          节点下线时摘解析。已签发的证书还在节点上、到期前照常能用，
          但没有服务商就<b>续不了期</b>。
        </p>
        <div class="actions">
          <button class="ghost" type="button" @click="askClear = false">取消</button>
          <button class="danger" type="button" :disabled="clearing" @click="clearProvider">
            {{ clearing ? '清除中…' : '确认清除' }}
          </button>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
@import './catalog.css';
@import './settings.css';
</style>
