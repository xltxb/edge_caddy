<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { http, errorText } from '@/api/http'
import type { CertWire, Paged } from '@/api/types'
import { challengeText } from '@/certs/challenge'
import { coverageText } from '@/certs/coverage'
import { fmtDate } from '@/utils/format'

/**
 * 证书。
 *
 * 「N / M 个节点」两列真相：N = Agent 回执里真正加载了这张证书的节点数，
 * M = 主控签发记录上应覆盖的节点数。**证书清单是回执，不是账本**
 * （CONTEXT.md）。N < M 意味着「下发到了但没生效」——这类故障在只显示
 * 一个数字的界面里是完全隐形的。
 */
const items = ref<CertWire[]>([])
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
    <div v-else-if="!items.length" class="hint">
      还没有证书。证书不再由主控签发 —— 要在外部平台签好之后导入进来，
      在这之前用到 HTTPS 的路由不会有可用的证书。
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
          </tr>
          <tr v-if="openRow === c.domain" class="detail-row">
            <td colspan="5">
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
