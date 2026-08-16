<script setup lang="ts">
import { onMounted } from 'vue'
import { useAlertsStore } from '@/stores/alerts'

const a = useAlertsStore()
onMounted(() => a.load())

const LEVELS: { v: 'all' | 'warn' | 'crit'; label: string; hint: string }[] = [
  { v: 'all', label: '全部', hint: '包含恢复与信息，量最大' },
  { v: 'warn', label: '异常及以上', hint: '推荐' },
  { v: 'crit', label: '仅严重', hint: '只有掉线这类必须立刻处理的' },
]
</script>

<template>
  <div class="wrap">
    <section class="card head">
      <span>告警通知</span>
      <div class="acts">
        <button class="btn ghost" :disabled="a.testing" @click="a.sendTest()">
          {{ a.testing ? '发送中…' : '发送测试' }}
        </button>
        <button class="btn" :disabled="a.saving" @click="a.save()">
          {{ a.saving ? '保存中…' : '保存' }}
        </button>
      </div>
    </section>

    <section v-if="a.error" class="card pad err">{{ a.error }}</section>
    <section v-if="a.testResult.msg" class="card pad" :class="{ err: !a.testResult.ok }">
      {{ a.testResult.msg }}
    </section>

    <section class="card pad">
      <label class="row">
        <input v-model="a.form.enabled" type="checkbox" />
        <span>启用告警通知</span>
      </label>

      <div class="lbl">通知级别</div>
      <div class="levels">
        <label v-for="l in LEVELS" :key="l.v" class="lv" :class="{ on: a.form.min_level === l.v }">
          <input v-model="a.form.min_level" type="radio" :value="l.v" />
          <span class="lv-t">{{ l.label }}</span>
          <span class="lv-h">{{ l.hint }}</span>
        </label>
      </div>
      <!-- 恢复通知的行为要说明白，否则「仅严重」会让人以为恢复也收不到 -->
      <p class="note">
        无论选哪一档，<strong>已报过警的节点恢复时都会收到一条闭环通知</strong>——
        否则群里永远挂着一条没有下文的告警。
      </p>

      <div class="lbl">重试次数</div>
      <input v-model.number="a.form.max_retries" class="in num" type="number" min="0" max="5" />
      <p class="note">上限 5 次。重试也是流量，而告警拖久了就没用了。</p>
    </section>

    <section class="card pad">
      <div class="lbl">通用 Webhook</div>
      <div class="cred">
        <input
          v-model="a.form.webhook_url"
          class="in"
          type="password"
          autocomplete="off"
          :placeholder="a.webhookConfigured ? '已配置（留空表示不改动）' : 'https://…'"
        />
        <button v-if="a.webhookConfigured" class="btn ghost sm" @click="a.clearWebhook()">清除</button>
      </div>

      <div class="lbl">Lark 群机器人</div>
      <div class="cred">
        <input
          v-model="a.form.lark_url"
          class="in"
          type="password"
          autocomplete="off"
          :placeholder="a.larkConfigured ? '已配置（留空表示不改动）' : 'https://open.larksuite.com/open-apis/bot/v2/hook/…'"
        />
        <button v-if="a.larkConfigured" class="btn ghost sm" @click="a.clearLark()">清除</button>
      </div>
      <div class="cred">
        <input
          v-model="a.form.lark_secret"
          class="in"
          type="password"
          autocomplete="off"
          :placeholder="a.larkSigned ? '签名密钥已配置（留空表示不改动）' : '签名密钥（开了签名校验才需要）'"
        />
        <button v-if="a.larkSigned" class="btn ghost sm" @click="a.clearLarkSecret()">清除</button>
      </div>

      <label class="row mt">
        <input v-model="a.form.at_all_on_crit" type="checkbox" />
        <span>严重告警 @所有人</span>
      </label>
      <p class="note">只对严重告警生效。警告也 @ 的话，很快就没人看了。</p>

      <!-- 凭据只写入不回显：界面上只能看到「配没配」 -->
      <p class="note warn">
        凭据保存后<strong>不再回显</strong>。Webhook 地址本身就是凭据，谁拿到都能往群里发消息。
      </p>
    </section>

    <section class="card pad stats">
      <div><span>已发送</span><b class="mono">{{ a.stats.sent }}</b></div>
      <div><span>失败</span><b class="mono" :class="{ bad: a.stats.failed > 0 }">{{ a.stats.failed }}</b></div>
      <div><span>队列丢弃</span><b class="mono" :class="{ bad: a.stats.dropped > 0 }">{{ a.stats.dropped }}</b></div>
    </section>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 14px; max-width: 760px; }
.card { background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; }
.head { display: flex; align-items: center; justify-content: space-between; padding: 12px 16px; font-weight: 600; color: var(--text-strong); }
.acts { display: flex; gap: 8px; }
.pad { padding: 14px 16px; }
.err { color: var(--danger-text); font-size: 13px; }
.btn { padding: 6px 13px; border: 0; border-radius: 8px; cursor: pointer; background: var(--accent); color: var(--text-on-accent); font-size: 12.5px; font-weight: 600; }
.btn.ghost { background: transparent; border: 1px solid var(--border-subtle); color: var(--text-body); }
.btn.sm { padding: 5px 10px; font-size: 11.5px; }
.row { display: flex; align-items: center; gap: 8px; font-size: 13px; color: var(--text-body); }
.row.mt { margin-top: 14px; }
.lbl { margin: 16px 0 7px; font-size: 10px; letter-spacing: .08em; text-transform: uppercase; color: var(--text-faint); font-weight: 600; }
.levels { display: flex; gap: 8px; flex-wrap: wrap; }
.lv { display: flex; flex-direction: column; gap: 2px; padding: 9px 12px; border: 1px solid var(--border-subtle); border-radius: 10px; cursor: pointer; min-width: 150px; }
.lv.on { border-color: var(--accent); background: var(--accent-subtle); }
.lv input { display: none; }
.lv-t { font-size: 13px; font-weight: 600; color: var(--text-strong); }
.lv-h { font-size: 11px; color: var(--text-muted); }
.in { width: 100%; box-sizing: border-box; padding: 7px 10px; border: 1px solid var(--border-subtle); border-radius: 8px; background: var(--surface-sunken); color: var(--text-strong); font-size: 12.5px; }
.in.num { width: 100px; }
.cred { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; }
.note { margin: 6px 0 0; font-size: 11.5px; line-height: 1.6; color: var(--text-muted); }
.note.warn { color: var(--danger-text); }
.stats { display: flex; gap: 28px; }
.stats div { display: flex; flex-direction: column; gap: 3px; }
.stats span { font-size: 10px; letter-spacing: .08em; text-transform: uppercase; color: var(--text-faint); font-weight: 600; }
.stats b { font-size: 17px; color: var(--text-strong); }
.stats b.bad { color: var(--danger-text); }
</style>
