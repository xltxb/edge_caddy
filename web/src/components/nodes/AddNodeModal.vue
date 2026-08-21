<script setup lang="ts">
import { ref } from 'vue'
import { errorText } from '@/api/http'
import { useNodesStore } from '@/stores/nodes'
import type { NodeTokenWire } from '@/api/types'
import { fmtClock } from '@/utils/format'

const emit = defineEmits<{ (e: 'close'): void }>()
const nodes = useNodesStore()

const form = ref({ node_id: '', city: '', vendor: '', line: '', public_ip: '' })
const issued = ref<NodeTokenWire | null>(null)
const busy = ref(false)
const error = ref('')

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

/**
 * 两条命令各有各的复制按钮。
 *
 * 一个「复制命令」按钮配两条命令，人无从知道复制到的是哪条 —— 而两条的时机
 * 完全不同（一条现在跑，一条装完跑）。也不合并成一次复制：把两行一起粘进
 * shell，install 失败时 verify 照样会跑，给出一个跟真正的失败无关的结果。
 */
const copiedKey = ref<'install' | 'verify' | null>(null)

async function copy(which: 'install' | 'verify'): Promise<void> {
  if (!issued.value) return
  const text = which === 'install' ? issued.value.install_cmd : issued.value.verify_cmd
  try {
    await navigator.clipboard.writeText(text)
    copiedKey.value = which
    setTimeout(() => (copiedKey.value = null), 2000)
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
        <div class="cmd-foot">
          <span>在目标机器上跑这条</span>
          <button class="ghost" type="button" @click="copy('install')">
            {{ copiedKey === 'install' ? '已复制' : '复制' }}
          </button>
        </div>

        <!--
          verify 与 install 并排，不折叠进「高级」。它查的是 Caddy Admin 有没有
          暴露在回环之外 —— 私钥以 load_pem 内联在运行配置里（ADR-0010），能读
          Admin 就能读到。脚本为「没在监听」和「监听错地方」分了两个返回值，
          而**一道没有人会执行的检查，等于不存在**。
        -->
        <pre class="cmd">{{ issued.verify_cmd }}</pre>
        <div class="cmd-foot">
          <span>装完再跑这条 —— 它会真的去查 Agent 接没接上、Caddy Admin 有没有监听在不该监听的地方</span>
          <button class="ghost" type="button" @click="copy('verify')">
            {{ copiedKey === 'verify' ? '已复制' : '复制' }}
          </button>
        </div>
        <div class="meta">
          <!--
            有效期读 expires_at，不写死「30 分钟」。那是主控的策略，会变；
            写死的数字变错了不会有任何报错，只会有人照着它算，然后发现 Token
            早就过期了。
          -->
          <span>有效至 {{ fmtClock(issued.expires_at) }} · 用后即失效</span>
        </div>
        <!--
          前提清单**由后端给**（prerequisites），不在这里硬编码，也不按当前长度
          写死 —— 它是「你必须自己先办好的事」，会随脚本增减。早先我把这两条
          自己写在这里，那就是两处知识。

          下面两条是它没有、也不该有的：一条是这台机器的出网能力，一条是命令
          文本本身的读法。两者都不是脚本能替人办的事。

          「机器上已安装官方 Caddy」曾经写在这里，是错的 —— 脚本自己会装
          （认不出发行版会直接失败并说清楚）。把脚本已经办了的事写成人要办的
          前提，会让人白做一遍，也会让真正要办的那两条被稀释。
        -->
        <ul class="checklist">
          <li v-for="p in issued.prerequisites" :key="p">{{ p }}</li>
          <li>目标机器可以外连主控的 9000 端口（Agent 主动外连，无需入站放行）</li>
          <li>
            命令里的 <code>--ca-pin</code> 是主控的 CA 指纹，<b>不要改也不要省</b>：
            少了它，首连就是纯 TOFU，中间人可以在那一刻把 Token 骗走。
          </li>
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
.cmd-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
  margin-top: -2px;
  font-size: var(--fs-micro);
  color: var(--text-muted);
  line-height: 1.6;
}
.checklist code {
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  color: var(--text-body);
  background: var(--surface-sunken, var(--bg-subtle));
  padding: 1px 4px;
  border-radius: var(--radius-sm);
}
.checklist b {
  color: var(--warning-text);
  font-weight: var(--weight-semibold);
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
