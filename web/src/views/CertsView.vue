<script setup lang="ts">
import { onMounted } from 'vue'
import { useCertsStore } from '@/stores/certs'

const c = useCertsStore()
onMounted(() => c.load())

function expiryText(daysLeft: number): string {
  if (daysLeft < 0) return `已过期 ${-daysLeft} 天`
  if (daysLeft === 0) return '今天到期'
  return `还有 ${daysLeft} 天`
}
</script>

<template>
  <div class="wrap">
    <section class="card head">
      <span>证书</span>
      <button class="btn" :disabled="c.busy !== ''" @click="c.renewAll()">
        {{ c.busy === '*' ? '检查中…' : '全部续期检查' }}
      </button>
    </section>

    <section v-if="c.error" class="card pad err">{{ c.error }}</section>

    <section v-if="c.results.length" class="card pad log">
      <div v-for="(r, i) in c.results" :key="i" class="lr" :class="{ bad: !r.ok }">
        <span class="mono">{{ r.domain }}</span>
        <span>{{ r.detail }}</span>
      </div>
    </section>

    <section class="card">
      <div class="thead mono">
        <span>域名</span><span>到期</span><span>签发者</span><span>密钥</span>
        <span>覆盖节点</span><span>续期</span><span style="text-align:right">操作</span>
      </div>
      <div v-if="c.loading" class="pad muted">载入中…</div>
      <div v-else-if="!c.certs.length" class="pad muted">
        还没有证书。配好 DNS 服务商后，为域名签发即可（系统设置 → DNS 服务商）。
      </div>
      <div v-for="cert in c.certs" :key="cert.domain" class="row" :data-domain="cert.domain">
        <span class="mono dm">{{ cert.domain }}</span>
        <!-- 「色 + 文字」双编码：只有颜色的话，色觉障碍的人看到的是三块一样的灰 -->
        <span class="exp" :class="cert.severity">
          <i class="dot" />{{ expiryText(cert.days_left) }} · {{ c.label(cert.severity) }}
        </span>
        <span class="mut">{{ cert.issuer || '—' }}</span>
        <span class="mut mono">{{ cert.key_type || '—' }}</span>
        <span class="mut">{{ cert.node_count }} 台</span>
        <span class="mut">自动</span>
        <span style="text-align:right">
          <button class="op" :disabled="c.busy !== ''" @click="c.renew(cert.domain)">
            {{ c.busy === cert.domain ? '续期中…' : '续期' }}
          </button>
        </span>
        <!-- 陈旧程度必须可见：不假装是最新的 -->
        <div v-if="cert.has_stale" class="stale" :data-stale="cert.domain">
          ⚠ {{ c.staleHint(cert) }}
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 14px; }
.card { background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; }
.head { display: flex; align-items: center; justify-content: space-between; padding: 12px 16px; font-weight: 600; color: var(--text-strong); }
.pad { padding: 14px 16px; }
.err { color: var(--danger-text); font-size: 13px; }
.muted { color: var(--text-muted); font-size: 13px; }
.btn { padding: 6px 13px; border: 0; border-radius: 8px; cursor: pointer; background: var(--accent); color: var(--text-on-accent); font-size: 12.5px; font-weight: 600; }
.btn:disabled { opacity: .55; cursor: default; }
.log { display: flex; flex-direction: column; gap: 5px; }
.lr { display: grid; grid-template-columns: 200px 1fr; gap: 10px; font-size: 11.5px; color: var(--text-muted); }
.lr.bad span:last-child { color: var(--danger-text); }
.thead, .row { display: grid; grid-template-columns: minmax(0,1.4fr) 190px 130px 110px 80px 60px 90px; gap: 12px; padding: 9px 16px; border-bottom: 1px solid var(--border-subtle); align-items: center; }
.thead { font-size: 10px; letter-spacing: .08em; text-transform: uppercase; color: var(--text-faint); font-weight: 600; }
.dm { font-size: 12.5px; font-weight: 600; color: var(--text-strong); }
.mut { font-size: 11.5px; color: var(--text-muted); }
.exp { font-size: 11.5px; display: flex; align-items: center; gap: 6px; }
.exp .dot { width: 7px; height: 7px; border-radius: 50%; display: inline-block; }
.exp.crit { color: var(--danger-text); } .exp.crit .dot { background: var(--danger-text); }
.exp.warn { color: var(--warn-text, #b26a00); } .exp.warn .dot { background: var(--warn-text, #b26a00); }
.exp.ok { color: var(--text-muted); } .exp.ok .dot { background: var(--ok-text, #2e7d32); }
.op { padding: 3px 10px; border: 1px solid var(--border-subtle); border-radius: 7px; background: transparent; color: var(--text-body); font-size: 11.5px; cursor: pointer; }
.op:disabled { opacity: .5; cursor: default; }
.stale { grid-column: 1 / -1; font-size: 11px; color: var(--warn-text, #b26a00); padding-top: 2px; }
</style>
