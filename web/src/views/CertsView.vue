<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { http, errorText } from '@/api/http'
import { useUiStore } from '@/stores/ui'
import type { CertDeleteWire, CertWire, Paged, SettingsWire } from '@/api/types'
import { challengeText } from '@/certs/challenge'
import { coverageText } from '@/certs/coverage'
import { orphanNote } from '@/certs/orphan'
import DeleteCertConfirm from '@/components/certs/DeleteCertConfirm.vue'
import { ApiError } from '@/api/http'
import { CODE } from '@/api/types'
import { fmtDate } from '@/utils/format'

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
const loading = ref(false)
const error = ref<string | null>(null)
const openRow = ref<string | null>(null)

/**
 * 空态要说的两件事都在 `GET /settings` 里（契约 §9）：证书从哪来、那条链路通没通。
 *
 * **两者都只读**：`master_endpoint` 由主控启动配置决定，`cert_bot_token_configured`
 * 只从环境变量 `EC_CERT_BOT_TOKEN` 读 —— `PUT` 里发它会被严格绑定当场拒掉。
 * （`EC_OPS_BOT_TOKEN` 是另一个：那是 ops-bot 的凭据，主控启动时会拒绝
 * 两者取同一个值。）
 * 所以它们在界面上是**陈述**，不是入口。
 */
const settings = ref<SettingsWire | null>(null)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    /*
     * 并行拉。settings 失败**不算加载失败** —— 证书列表本身跟它无关，
     * 为了空态里那两行附加信息把整页变成错误页是不成比例的。
     */
    const [certs, st] = await Promise.all([
      http.get<Paged<CertWire>>('/certs'),
      http.get<SettingsWire>('/settings').catch(() => null),
    ])
    items.value = certs.items
    settings.value = st
  } catch (e) {
    error.value = errorText(e, '加载证书失败')
  } finally {
    loading.value = false
  }
}

onMounted(load)

/**
 * 到期两档，**而分两档是有意的**。
 *
 *   < 14 天  红，且后端每天发一条告警 —— 「今天就得做」
 *   < 30 天  黄，**不发告警** —— 「安排一下」，主动来看才看得到
 *
 * 两档的边界与「哪一档发告警」都在**契约 §9**，不是这一页自己定的口径。
 *
 * 合成一档会把「还早」说成「来不及了」，于是前一种被当成后一种忽略掉 ——
 * **而那正好训练人在真的来不及时也忽略**。
 *
 * 主控不再签发也不再自动续期（**契约 §9**：证书只从外部平台导入），所以这两档
 * 现在是**唯一**会提醒人去续证书的东西。此前那句「≤7 天危」对应的是自动续期
 * 兜底之下的余量，而那个兜底没有了。
 */
function level(days: number): 'crit' | 'warn' | 'ok' {
  return days < 14 ? 'crit' : days < 30 ? 'warn' : 'ok'
}

/* ── 删除 ────────────────────────────────────────────────────────────── */

const delTarget = ref<CertWire | null>(null)
const delBusy = ref(false)
/** 被 2001 拒了 —— 后端那句话列出了它在服务哪些域名，原样交给弹层。 */
const delRefused = ref<string | null>(null)
/** 删成了 —— detail 原样显示，由人自己关掉。 */
const delResult = ref<string | null>(null)

function closeDelete(): void {
  delTarget.value = null
  delBusy.value = false
  delRefused.value = null
  delResult.value = null
}

/**
 * `force` 由弹层给，不由这里推。
 *
 * 界面**不预判**「这张能不能删」——那道判据在后端（`covers` 非空 → 2001），
 * 而那一份看得到路由清单。前端复刻一份的话，`covers` 为 `null`（算不出来）
 * 那一支就会变成前端替人做的决定。
 */
async function onDelete(force: boolean): Promise<void> {
  const c = delTarget.value
  if (!c) return
  delBusy.value = true
  delRefused.value = null
  try {
    /*
     * **query 拼在模板字面量外面，路径本身留在调用里。**
     *
     * `check-requests` 扫的是调用处的字面量（`http.del(\`…\`)`），把整个路径存进
     * 一个变量会让它扫不到 —— 那条登记就成了「登记了但没人调」。而把
     * `?force=true` 写进字面量里也不行：它的 `normalize` 不剥 query，
     * 键会变成 `/certs/:x?force=true`，跟登记的对不上。
     *
     * 这是那道检查的写法要求，不是风格偏好 —— 写错了它会红，而红的判词说的是
     * 「没人调」，离真因（写法）有一步。
     */
    const q = force ? '?force=true' : ''
    const r = await http.del<CertDeleteWire>(`/certs/${encodeURIComponent(c.domain)}` + q)
    delResult.value = r.detail
    items.value = items.value.filter((x) => x.domain !== c.domain)
  } catch (e) {
    /*
     * **2001 不是失败，是一条出路。** 它说「这张还在服务，确实要删就带 force」——
     * 把它跟别的错误一样丢进 toast，人就再也看不到那条出路了。
     */
    if (e instanceof ApiError && e.code === CODE.STATE_CONFLICT) {
      delRefused.value = e.message
    } else {
      ui.toast('warn', '删除失败', errorText(e, ''))
      closeDelete()
    }
  } finally {
    delBusy.value = false
  }
}

const COLOR = { crit: 'var(--danger)', warn: 'var(--warning)', ok: 'var(--success)' }
const TEXT = { crit: 'var(--danger-text)', warn: 'var(--warning-text)', ok: 'var(--text-strong)' }

/** 红档：14 天内到期，后端每天一条告警。 */
const urgent = computed(() => items.value.filter((c) => c.days_left < 14))
/** 黄档：30 天内但还没进红档。**不发告警，所以只有这一行会说它。** */
const soon = computed(() => items.value.filter((c) => c.days_left >= 14 && c.days_left < 30))
const mismatched = computed(() => items.value.filter((c) => c.loaded_nodes < c.expected_nodes))
</script>

<template>
  <section class="panel">
    <header class="head">
      <div class="title">证书</div>
      <!--
        **「由主控集中签发（DNS-01）」是假的。** 主控不再签发也不再续期，
        证书只从外部平台导入 —— 这一页从「看主控签发了什么」变成了
        「你导入了什么，以及哪些快到期了」。
      -->
      <div class="sub">共 {{ items.length }} 张 · 从外部平台导入</div>
    </header>

    <!--
      **两档分开说，因为它们要人做的事不同。**

      原来这里写的是「N 张将在 14 天内到期，其中 M 张未开启自动续期，需要手动
      处理」。后半句现在错得很特别：它把「需要手动处理」说成一个**子集**的属性，
      而拆掉自动续期之后那是**全集**的属性 —— 字面仍然成立（那 M 张确实需要
      手动处理），而**它暗示的对比不存在了**。这类句子比直接说错更难发现。

      黄档单独一行、不用警示色：它不发告警，**这一行是它唯一会被看见的地方**。
    -->
    <div v-if="urgent.length" class="banner danger">
      {{ urgent.length }} 张证书将在 14 天内到期，需要去外部平台续期后重新导入。
    </div>
    <div v-if="soon.length" class="banner warn">
      另有 {{ soon.length }} 张在 30 天内到期 —— 还不紧急，但这一档不发告警，
      只有在这一页看得到。
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
      **这块地方的意思整个反过来了。**

      原来写的是「证书由主控在下发时自动签发，所以第一次下发之前这里是空的」——
      它不只是描述错了，它还**解释了为什么空**，而那个解释会让人得出「我什么都
      不用做，下发之后自然就有」。

      现在空着恰恰是因为**没人导入过，而不导入它会一直空着**（契约 §9：
      证书只从外部平台导入，`PUT /certs/:domain` 是唯一入口）。
      同一块地方，从「不用管」变成了「这里就是你该动手的地方」。
    -->
    <div v-else-if="!items.length" class="empty">
      <p class="lead">
        还没有证书。证书由<b>外部证书平台推送</b>到主控 ——
        控制台里没有上传的地方，<b>这是有意的</b>，不是还没做。
      </p>

      <!--
        **这两行是陈述，不是入口。**

        `master_endpoint` 由主控启动配置决定（它进了服务端证书的 SAN，运行时改不了）；
        `cert_bot_token_configured` 只从环境变量 `EC_CERT_BOT_TOKEN` 读，`PUT` 里
        发它会被严格绑定当场拒掉。两者在控制台里都改不了。

        所以**不能做成看起来能点的样子** —— 一个标出来却点不动的东西，第一次让人
        去找按钮找不到，第二次他就学会跳过这类提示了。用纯文本、标「只读」。
      -->
      <dl class="link-facts">
        <dt>推送地址</dt>
        <dd class="mono">
          {{ settings?.master_endpoint || '—' }}
          <span class="ro">只读 · 由主控启动配置决定</span>
        </dd>
        <dt>推送认证</dt>
        <dd>
          <span :class="settings?.cert_bot_token_configured ? 'okc' : 'warn'">
            {{ settings?.cert_bot_token_configured ? '已配置' : '未配置' }}
          </span>
          <span class="ro">只读 · 从环境变量读，控制台改不了</span>
          <!--
            未配置时这条链路是断的 —— 而症状是「证书一直不来」，
            那句话本身不会指向这里。
          -->
          <p v-if="settings && !settings.cert_bot_token_configured" class="warn small">
            没配的话外部平台推不进来，而症状只是「证书一直没出现」。
          </p>
        </dd>
      </dl>

      <!--
        **代价说出来，不藏着。** 契约 §9 明写了这一条：没有上传表单意味着
        外部平台挂了、或临时要换一张证书时，控制台里做不了。
        说清那时候走哪条路，人才不会去数据库里翻。
      -->
      <p class="note">
        外部平台挂了、或者临时要换一张证书时，<b>控制台里做不了</b> ——
        那时直接调 <code>PUT /certs/:domain</code>（curl + 上面那个 token），
        不要去改数据库：绕过接口之后不会触发下发，节点上的证书不会变。
      </p>

      <RouterLink class="mini" to="/routes">去看反代路由</RouterLink>
    </div>

    <table v-else class="table">
      <thead>
        <tr>
          <th>域名</th>
          <th>签发者</th>
          <th>来源</th>
          <th>剩余有效期</th>
          <th>节点（回执 / 签发）</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <template v-for="c in items" :key="c.domain">
          <tr>
            <td>
              <div class="mono strong">{{ c.domain }}</div>
              <!--
                摘要够扫，**完整列表要留得住**：`*.a.com` 不覆盖 `x.y.a.com`
                （RFC 6125，通配符只匹配一级），所以「通配符」三个字回答不了
                「我这个域名在不在里面」。全量挂在 title 上。
              -->
              <div class="muted small" :title="c.domains?.join('、')">
                {{ coverageText(c.domains) }}
              </div>
              <!--
                **只有「没人用」会说话**（契约 §9）。

                `covers` 三种值三个意思：有值 = 正常（不说）；`[]` = 存着但没人用
                （说 —— 这一列是唯一说得出这件事的地方）；`null` = 算不出来
                （**不说** —— 把它渲染成「没人用」会引着人去删一张可能还在服务
                二十条路由的证书）。判断在 @/certs/orphan，有证伪测试。
              -->
              <div v-if="orphanNote(c.covers)" class="orphan">
                {{ orphanNote(c.covers)!.text }}
              </div>
            </td>
            <td class="muted">{{ c.issuer }}</td>
            <td class="muted small">{{ challengeText(c.challenge) }}</td>
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
              <!--
                **天数好扫，日期用来跟告警对上。**

                到期告警的文案是「还有 12 天到期（2026-09-04）」（契约 §9），
                而列表里只有天数 —— 人拿着告警来对界面时，中间那步换算得他自己做。
                日期常驻在 title 上；**只在红黄两档显示出来**，因为只有那两档
                会发告警，而正常的证书不需要占这一行。
              -->
              <div
                class="mono days"
                :style="{ color: TEXT[level(c.days_left)] }"
                :title="`到期 ${fmtDate(c.not_after)}`"
              >
                {{ c.days_left }} 天
                <span v-if="level(c.days_left) !== 'ok'" class="muted">
                  · {{ fmtDate(c.not_after) }}
                </span>
              </div>
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
              <!--
                **不置灰。** 跟节点的「删除记录」不同：那一条是前提（不先下线就会
                产生一台看不见的机器），而这里「还在服务」是一个人可能真的想推翻的
                判断 —— 后端会拒（2001）并给出 force 那条路。置灰等于替人否掉它。
              -->
              <button class="mini danger" type="button" @click="delTarget = c">删除</button>
            </td>
          </tr>
          <tr v-if="openRow === c.domain" class="detail-row">
            <td colspan="6">
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

  <DeleteCertConfirm
    v-if="delTarget"
    :cert="delTarget"
    :busy="delBusy"
    :refused="delRefused"
    :result="delResult"
    @cancel="closeDelete"
    @confirm="onDelete"
  />
</template>

<style scoped>
@import './catalog.css';
.head .sub {
  margin-right: auto;
}
.small {
  font-size: var(--fs-micro);
}
.orphan {
  font-size: var(--fs-micro);
  color: var(--text-faint);
  margin-top: 2px;
}
.empty {
  padding: var(--space-6) var(--space-5);
  max-width: 620px;
}
.empty .lead {
  margin: 0 0 var(--space-4);
  font-size: var(--fs-xs);
  color: var(--text-body);
  line-height: 1.8;
}
.empty .lead b {
  color: var(--text-strong);
  font-weight: var(--weight-semibold);
}
.link-facts {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: var(--space-2) var(--space-4);
  margin: 0 0 var(--space-4);
  font-size: var(--fs-2xs);
}
.link-facts dt {
  color: var(--text-faint);
}
.link-facts dd {
  margin: 0;
  color: var(--text-body);
}
.link-facts .ro {
  margin-left: var(--space-2);
  font-size: var(--fs-micro);
  color: var(--text-faint);
}
.link-facts .okc {
  color: var(--success-text, var(--accent));
}
.link-facts .warn {
  color: var(--warning-text);
}
.link-facts p {
  margin: var(--space-1) 0 0;
  line-height: 1.7;
}
.empty .note {
  margin: 0 0 var(--space-4);
  font-size: var(--fs-2xs);
  color: var(--text-muted);
  line-height: 1.8;
}
.empty .note b {
  color: var(--warning-text);
  font-weight: var(--weight-semibold);
}
.empty .note code {
  font-family: var(--font-mono);
  background: var(--surface-sunken);
  padding: 1px 4px;
  border-radius: var(--radius-sm);
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
