<script setup lang="ts">
import { onMounted } from 'vue'
import { useDnsProviderStore, LE_STAGING, LE_PRODUCTION } from '@/stores/dnsprovider'

const p = useDnsProviderStore()
onMounted(() => p.load())
</script>

<template>
  <div class="wrap">
    <section class="card head">
      <span>DNS 服务商与证书签发</span>
      <button class="btn" :disabled="p.saving" @click="p.save()">
        {{ p.saving ? '保存中…' : '保存' }}
      </button>
    </section>

    <section v-if="p.error" class="card pad err">{{ p.error }}</section>

    <!-- staging 必须显眼：那上面签出来的证书浏览器不认，而
         「证书装好了但浏览器还是报警告」查起来很费时 -->
    <section v-if="p.staging" class="card pad warnbox" data-testid="staging-hint">
      {{ p.stagingHint() }}
    </section>

    <section class="card pad">
      <div class="lbl">服务商</div>
      <div class="kinds">
        <label class="kind" :class="{ on: p.form.kind === 'dnspod' }">
          <input v-model="p.form.kind" type="radio" value="dnspod" />
          <span class="kt">DNSPod</span>
          <span class="kh">支持线路与加权解析</span>
        </label>
        <label class="kind" :class="{ on: p.form.kind === 'cloudflare' }">
          <input v-model="p.form.kind" type="radio" value="cloudflare" />
          <span class="kt">Cloudflare</span>
          <!-- 如实说不支持，而不是悄悄按等权重写下去 -->
          <span class="kh">仅签发证书；无线路概念，不支持加权解析</span>
        </label>
      </div>

      <template v-if="p.form.kind === 'dnspod'">
        <div class="lbl">DNSPod ID</div>
        <div class="cred">
          <input v-model="p.form.dnspod_id" class="in" type="password" autocomplete="off"
                 :placeholder="p.dnspodConfigured ? '已配置（留空表示不改动）' : '例如 12345'" />
        </div>
        <div class="lbl">DNSPod Token</div>
        <div class="cred">
          <input v-model="p.form.dnspod_token" class="in" type="password" autocomplete="off"
                 :placeholder="p.dnspodConfigured ? '已配置（留空表示不改动）' : 'API Token'" />
          <button v-if="p.dnspodConfigured" class="btn ghost sm" @click="p.clearDnspod()">清除</button>
        </div>
      </template>

      <template v-if="p.form.kind === 'cloudflare'">
        <div class="lbl">Cloudflare API Token</div>
        <div class="cred">
          <input v-model="p.form.cloudflare_token" class="in" type="password" autocomplete="off"
                 :placeholder="p.cloudflareConfigured ? '已配置（留空表示不改动）' : 'API Token（不是 Global API Key）'" />
          <button v-if="p.cloudflareConfigured" class="btn ghost sm" @click="p.clearCloudflare()">清除</button>
        </div>
        <p class="note">
          只接受 API Token。Global API Key 等于整个账号的全部权限，一旦泄漏连账单都能改。
        </p>
      </template>

      <div class="lbl">ACME 联系邮箱</div>
      <input v-model="p.form.acme_email" class="in" type="email" placeholder="ops@example.com" />
      <p class="note">到期提醒与吊销通知会发到这里。它不是凭据，所以界面上看得到。</p>

      <div class="lbl">ACME 环境</div>
      <div class="kinds">
        <label class="kind" :class="{ on: p.form.acme_directory === LE_STAGING }">
          <input v-model="p.form.acme_directory" type="radio" :value="LE_STAGING" />
          <span class="kt">Staging（推荐先用）</span>
          <span class="kh">证书浏览器不认，但可以随便试</span>
        </label>
        <label class="kind" :class="{ on: p.form.acme_directory === LE_PRODUCTION }">
          <input v-model="p.form.acme_directory" type="radio" :value="LE_PRODUCTION" />
          <span class="kt">正式环境</span>
          <span class="kh">每个域名每周只能签 5 张</span>
        </label>
      </div>

      <p class="note warn">
        凭据保存后<strong>不再回显</strong>。DNS API 凭据能改写整个 zone，
        因此它只存在于主控——边缘节点上没有它（ADR-0001）。
      </p>
    </section>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 14px; max-width: 760px; }
.card { background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; }
.head { display: flex; align-items: center; justify-content: space-between; padding: 12px 16px; font-weight: 600; color: var(--text-strong); }
.pad { padding: 14px 16px; }
.err { color: var(--danger-text); font-size: 13px; }
.warnbox { font-size: 12.5px; line-height: 1.7; color: var(--warn-text, #b26a00); }
.btn { padding: 6px 13px; border: 0; border-radius: 8px; cursor: pointer; background: var(--accent); color: var(--text-on-accent); font-size: 12.5px; font-weight: 600; }
.btn.ghost { background: transparent; border: 1px solid var(--border-subtle); color: var(--text-body); }
.btn.sm { padding: 5px 10px; font-size: 11.5px; }
.lbl { margin: 16px 0 7px; font-size: 10px; letter-spacing: .08em; text-transform: uppercase; color: var(--text-faint); font-weight: 600; }
.kinds { display: flex; gap: 8px; flex-wrap: wrap; }
.kind { display: flex; flex-direction: column; gap: 2px; padding: 9px 12px; border: 1px solid var(--border-subtle); border-radius: 10px; cursor: pointer; min-width: 210px; }
.kind.on { border-color: var(--accent); background: var(--accent-subtle); }
.kind input { display: none; }
.kt { font-size: 13px; font-weight: 600; color: var(--text-strong); }
.kh { font-size: 11px; color: var(--text-muted); }
.in { width: 100%; box-sizing: border-box; padding: 7px 10px; border: 1px solid var(--border-subtle); border-radius: 8px; background: var(--surface-sunken); color: var(--text-strong); font-size: 12.5px; }
.cred { display: flex; gap: 8px; align-items: center; }
.note { margin: 6px 0 0; font-size: 11.5px; line-height: 1.6; color: var(--text-muted); }
.note.warn { color: var(--danger-text); margin-top: 16px; }
</style>
