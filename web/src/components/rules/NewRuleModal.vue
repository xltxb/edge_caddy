<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RESOURCE_ID, RESOURCE_ID_HINT } from '@/ids'
import { useRouter } from 'vue-router'
import { errorText } from '@/api/http'
import type { RuleType } from '@/api/types'
import { TYPE_LABEL } from '@/rules/summary'
import { useConfigStore } from '@/stores/config'
import { useUiStore } from '@/stores/ui'

/**
 * 新建访问规则。
 *
 * ## 这个入口从前不存在，而那件事在 dev 里看不出来
 *
 * 七种规则的显示与编辑都做了，**唯独没有创建**：mock 的种子里预置了各类型
 * 各一条，于是 dev 下打开访问控制页，七种都在、都能编辑 ——
 * **看起来这一批是完整的。**
 *
 * 真实环境里那张表是空的，而没有任何地方能建出第一条。
 * 「后端做完了而界面上找不到」正是用户报过来的原话。
 *
 * > 种子数据会掩盖入口的缺失：验「显示对不对」永远验不出
 * > 「一个空系统能不能走到这个状态」。
 *
 * ## 建出来的是一条停用、未绑定的骨架
 *
 * **后端的校验只跑在启用且已绑定的规则上**（契约 §6.2），所以骨架存得进去 ——
 * 人接着去工作台配好、绑域名、再启用。后端那侧钉住了这一档
 * （`TestDisabledRuleAcceptsEmptySpec`），所以它不是一句会悄悄过期的话。
 *
 * 不在这里塞一个多字段表单：工作台里已经有一份更好的，
 * 而且每种类型的字段完全不同，塞进来等于把那七套表单再写一遍。
 */
const emit = defineEmits<{ (e: 'close'): void }>()
const router = useRouter()
const config = useConfigStore()
const ui = useUiStore()

/**
 * 各类型的初始 spec。
 *
 * 空的那几个（名单、特征）留空是对的 —— 让人去填。
 * 有默认值的那几个（限流的桶、地域的方向）给的是**后端认得的合法值**，
 * 不是「随便一个数」：一个填不满的骨架在工作台里会立刻报错，
 * 而人还没来得及做任何事。
 *
 * `geo_mode` 给 `block`：两个方向都得显式给（契约 §6.2 没有默认），
 * 而**拦名单里的**是错了危害较小的那一个 —— 猜成 `allow` 的话，
 * 一条空名单的规则一旦被启用就是「谁也放不进来」。
 */
const SKELETON: Record<RuleType, Record<string, unknown>> = {
  ip_whitelist: { ips: [] },
  ip_blacklist: { ips: [] },
  request_filter: { filters: [] },
  rate_limit: { requests: 100, window_s: 60, rate_key: 'ip' },
  geo_block: { geo_mode: 'block', geo_countries: [] },
  service_secret: {
    header: 'X-Service-Key',
    algo: 'hmac-sha256',
    ttl_s: 300,
    replay_protection: true,
  },
  jwt_bearer: { iss: '', aud: '', jwks_url: '', skew_s: 60 },
}

/**
 * 名称的例子，**跟着类型走**。
 *
 * 固定一个例子（原先写死的是「扫描器来源黑名单」）在选了别的类型时是**一句
 * 不相干的提示**：类型选的是限流，而占位符在教他怎么给黑名单起名字。
 * 占位符是这一格唯一的示范，示范错了比没有更费解。
 */
const NAME_EG: Record<RuleType, string> = {
  ip_whitelist: '办公出口白名单',
  ip_blacklist: '扫描器来源黑名单',
  request_filter: '探测路径与工具',
  rate_limit: '登录接口限流',
  geo_block: '仅中国大陆与港澳',
  service_secret: '合作方服务密钥',
  jwt_bearer: 'App 客户端 JWT',
}

/** 一句话说清这个类型是干什么的 —— 选之前就该知道，而不是建完再发现选错。 */
const TYPE_HINT: Record<RuleType, string> = {
  ip_whitelist: '只放名单里的来源，其余按域名的处置方式拦下。',
  ip_blacklist: '拦名单里的来源，其余放行。跟白名单是相反的两件事。',
  request_filter: '按路径 / UA / 头等特征拦。多条之间是「或」，命中任一即拦。',
  rate_limit: '令牌桶限流。计数每节点各算各的 —— 三台节点、每台 100，全局是 300。',
  geo_block: '按国家拦或放。需要主控把 GeoIP 库下发到节点，库没到时这条不生效。',
  service_secret: '第三方系统带签名头，由节点上的 Agent 验签。',
  jwt_bearer: '终端客户端带 JWT，由节点上的 Agent 验签。',
}

const TYPES = Object.keys(SKELETON) as RuleType[]

const type = ref<RuleType>('ip_blacklist')
const id = ref('')
const name = ref('')
const busy = ref(false)
const serverError = ref('')

/** 换类型时把没动过的 id 一起换掉 —— 但人改过就不动它。 */
const idTouched = ref(false)
const suggestedId = computed(() => type.value.replace(/_/g, '-'))
watch(type, () => {
  if (!idTouched.value) id.value = suggestedId.value
})
id.value = suggestedId.value

/**
 * **重名必须在这里拦住，因为后端不会拒。**
 *
 * `PUT /rules/:id` 是 upsert（契约 §6.2 —— 那一节没有 `POST /rules`，
 * 新建就是往一个不存在的 id 上 PUT）：填一个已存在的 id，它会把那条规则
 * **整个换掉**并回 `code: 0`。这跟路由不同（契约 §6.1：`POST /routes`
 * 重名回 `1004`）。
 *
 * 所以这一条不是「重名不好看」，是**一次静默的覆盖** ——
 * 人以为自己新建了一条，实际把别人配好的那条抹了，而两边都没有任何提示。
 */
const idError = computed(() => {
  const v = id.value.trim()
  if (!v) return ''
  if (!RESOURCE_ID.test(v)) {
    return RESOURCE_ID_HINT
  }
  if (config.rules.some((r) => r.id === v)) {
    return '已经有一条这个 ID 的规则 —— 用它保存会把那一条整个覆盖掉'
  }
  return ''
})

const canSubmit = computed(
  () => id.value.trim() !== '' && name.value.trim() !== '' && !idError.value && !busy.value,
)

async function submit(): Promise<void> {
  if (!canSubmit.value) return
  busy.value = true
  serverError.value = ''
  const ruleId = id.value.trim()
  try {
    await config.createRule(ruleId, {
      name: name.value.trim(),
      type: type.value,
      // 停用 + 未绑定 = 骨架存得进去（后端只校验启用且已绑定的）
      enabled: false,
      apply_to: [],
      spec: SKELETON[type.value],
    })
    emit('close')
    ui.toast('ok', '规则已建好', '它现在是停用的 —— 配好并绑上域名后再启用。')
    // 建完就得配，把人留在列表页只会让他再点一次（跟新建路由一致）
    void router.push({ name: 'workbench', params: { key: `rule:${ruleId}` } })
  } catch (e) {
    serverError.value = errorText(e, '创建失败')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="mask" @click.self="$emit('close')">
    <form class="modal" @submit.prevent="submit">
      <div class="title">新建访问规则</div>

      <label class="field">
        <span class="lbl">类型</span>
        <select v-model="type" class="text sel">
          <option v-for="t in TYPES" :key="t" :value="t">{{ TYPE_LABEL[t] }}</option>
        </select>
        <span class="hint">{{ TYPE_HINT[type] }}</span>
      </label>

      <label class="field">
        <span class="lbl">规则 ID</span>
        <input
          v-model="id"
          class="text mono"
          :class="{ bad: idError }"
          placeholder="scanner-block"
          @input="idTouched = true"
        />
        <!--
          ID 改不了这件事要说在前面：它是 `PUT /rules/:id` 的路径段，
          改名等于建一条新的再删旧的 —— 而那中间那条旧的还在生效。
        -->
        <span v-if="idError" class="err">{{ idError }}</span>
        <span v-else class="hint">建好之后改不了。它是这条规则在接口与审计里的名字。</span>
      </label>

      <label class="field">
        <span class="lbl">名称</span>
        <input v-model="name" class="text" :placeholder="NAME_EG[type]" />
        <span class="hint">给人看的。列表和下发记录里显示它。</span>
      </label>

      <p class="note">
        建好的规则是<b>停用</b>的，也没有绑定域名 —— 接着会跳到配置工作台，
        在那里填内容、勾域名、再启用。
      </p>

      <p v-if="serverError" class="err block">{{ serverError }}</p>

      <div class="actions">
        <button class="ghost" type="button" @click="$emit('close')">取消</button>
        <button class="primary" type="submit" :disabled="!canSubmit">
          {{ busy ? '创建中…' : '创建并去配置' }}
        </button>
      </div>
    </form>
  </div>
</template>

<style scoped>
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
.title {
  font-size: var(--fs-base);
  font-weight: var(--weight-bold);
  color: var(--text-strong);
  margin-bottom: var(--space-4);
}
.field {
  display: block;
  margin-bottom: var(--space-4);
}
.lbl {
  display: block;
  font-size: var(--fs-2xs);
  color: var(--text-muted);
  margin-bottom: var(--space-1);
}
.text {
  width: 100%;
  padding: 6px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-card);
  color: var(--text-strong);
  font-size: var(--fs-xs);
}
.text.bad {
  border-color: var(--danger-text);
}
.sel {
  font-family: inherit;
}
.hint,
.err {
  display: block;
  margin-top: var(--space-1);
  font-size: var(--fs-2xs);
  line-height: 1.6;
  color: var(--text-faint);
}
.err {
  color: var(--danger-text);
}
.err.block {
  margin: 0 0 var(--space-3);
}
.note {
  margin: 0 0 var(--space-4);
  padding: var(--space-3);
  border-radius: var(--radius-sm);
  background: var(--surface-sunken);
  font-size: var(--fs-2xs);
  line-height: 1.7;
  color: var(--text-muted);
}
.note b {
  color: var(--text-body);
}
.actions {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-2);
}
.ghost {
  padding: 6px 14px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-card);
  color: var(--text-strong);
  font-size: var(--fs-xs);
  cursor: pointer;
}
.primary {
  padding: 6px 14px;
  border: 1px solid var(--accent);
  border-radius: var(--radius-sm);
  background: var(--accent);
  color: var(--text-on-accent);
  font-size: var(--fs-xs);
  font-weight: var(--weight-semibold);
  cursor: pointer;
}
.primary:disabled {
  opacity: 0.5;
  cursor: default;
}
</style>
