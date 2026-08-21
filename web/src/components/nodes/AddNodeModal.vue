<script setup lang="ts">
import { ref } from 'vue'
import { errorText } from '@/api/http'
import { useNodesStore } from '@/stores/nodes'
import type { NodeTokenWire } from '@/api/types'

const emit = defineEmits<{ (e: 'close'): void }>()
const nodes = useNodesStore()

const form = ref({ node_id: '', city: '', vendor: '', line: '', public_ip: '' })
const issued = ref<NodeTokenWire | null>(null)
const busy = ref(false)
const error = ref('')
const copied = ref(false)

async function submit(): Promise<void> {
  busy.value = true
  error.value = ''
  try {
    issued.value = await nodes.issueToken({ ...form.value })
  } catch (e) {
    error.value = errorText(e, '签发失败')
  } finally {
    busy.value = false
  }
}

async function copy(): Promise<void> {
  if (!issued.value) return
  try {
    await navigator.clipboard.writeText(issued.value.install_cmd)
    copied.value = true
    setTimeout(() => (copied.value = false), 2000)
  } catch {
    // 没有剪贴板权限时命令仍然可见，用户可以自己选中复制
  }
}
</script>

<template>
  <div class="mask" @click.self="emit('close')">
    <div class="modal" role="dialog" aria-modal="true" aria-label="添加节点">
      <div class="title">添加边缘节点</div>

      <template v-if="!issued">
        <p class="lead">
          填写这台机器的信息，主控会签发一次性接入 Token。Token
          在签发时就绑定这台机器的身份。
        </p>
        <form class="form" @submit.prevent="submit">
          <label><span>节点 ID</span><input v-model="form.node_id" required placeholder="node-sg-01" /></label>
          <label><span>城市</span><input v-model="form.city" required placeholder="新加坡" /></label>
          <label><span>服务商</span><input v-model="form.vendor" required placeholder="V.PS" /></label>
          <label><span>线路</span><input v-model="form.line" required placeholder="CMIN2" /></label>
          <label><span>公网 IP</span><input v-model="form.public_ip" required placeholder="203.0.113.9" /></label>
          <p v-if="error" class="err">{{ error }}</p>
          <div class="actions">
            <button class="ghost" type="button" @click="emit('close')">取消</button>
            <button class="primary" type="submit" :disabled="busy">
              {{ busy ? '签发中…' : '签发接入 Token' }}
            </button>
          </div>
        </form>
      </template>

      <template v-else>
        <!--
          Token 只在这一次响应里出现，任何后续接口都不回显（契约 §4）。
          所以这个提示不是客套 —— 关掉之后真的再也拿不到。
        -->
        <p class="lead warn">
          这段命令只显示这一次。关闭后 Token 无法再取回，只能重新签发。
        </p>
        <pre class="cmd">{{ issued.install_cmd }}</pre>
        <div class="meta">
          <span>30 分钟内有效，用后即失效</span>
          <button class="ghost" type="button" @click="copy">
            {{ copied ? '已复制' : '复制命令' }}
          </button>
        </div>
        <ul class="checklist">
          <li>目标机器可以外连主控的 9000 端口（Agent 主动外连，无需入站放行）</li>
          <li>机器上已安装官方 Caddy，Admin 只监听 127.0.0.1:2019</li>
          <li>安装脚本会校验主控 CA 指纹，防止首连被中间人冒充</li>
        </ul>
        <div class="actions">
          <button class="primary" type="button" @click="emit('close')">完成</button>
        </div>
      </template>
    </div>
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
  width: min(520px, 100%);
  background: var(--surface-raised);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-xl);
  padding: var(--space-5);
  max-height: 86vh;
  overflow-y: auto;
}
.title {
  font-size: var(--fs-base);
  font-weight: var(--weight-bold);
  color: var(--text-strong);
  margin-bottom: var(--space-2);
}
.lead {
  margin: 0 0 var(--space-4);
  font-size: var(--fs-xs);
  color: var(--text-muted);
  line-height: 1.7;
}
.lead.warn {
  color: var(--warning-text);
}
.form {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
label {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}
label span {
  font-size: var(--fs-xs);
  color: var(--text-body);
}
input {
  padding: 7px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-card);
  color: var(--text-strong);
  font-size: var(--fs-xs);
  font-family: var(--font-mono);
}
input:focus {
  border-color: var(--accent);
}
.cmd {
  margin: 0 0 var(--space-2);
  padding: var(--space-3);
  border-radius: var(--radius-sm);
  background: var(--surface-sunken);
  border: 1px solid var(--border-subtle);
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-all;
  color: var(--text-body);
}
.meta {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
  font-size: var(--fs-micro);
  color: var(--text-faint);
  margin-bottom: var(--space-4);
}
.checklist {
  margin: 0 0 var(--space-4);
  padding-left: 18px;
  font-size: var(--fs-2xs);
  color: var(--text-muted);
  line-height: 1.9;
}
.err {
  margin: 0;
  font-size: var(--fs-2xs);
  color: var(--danger-text);
}
.actions {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-2);
}
.ghost {
  padding: 5px 12px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-card);
  color: var(--text-strong);
  font-size: var(--fs-micro);
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
</style>
