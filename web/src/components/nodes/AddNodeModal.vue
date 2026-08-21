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
          <!--
            有效期读 expires_at，不写死「30 分钟」。那是主控的策略，会变；
            写死的数字变错了不会有任何报错，只会有人照着它算，然后发现 Token
            早就过期了。
          -->
          <span>有效至 {{ fmtClock(issued.expires_at) }} · 用后即失效</span>
          <button class="ghost" type="button" @click="copy">
            {{ copied ? '已复制' : '复制命令' }}
          </button>
        </div>
        <!--
          这几条说的是**这条命令能跑起来的前提**，不是泛泛的最佳实践。

          头两条是同一类：命令里的 `./edge-node.sh` 和 `--agent-bin ./edge-agent`
          都是相对路径，都假定那个文件已经在当前目录。谁也不负责把它们送上去，
          而不说的话，这条「复制粘贴即可」的命令会在一台远程机器上以
          「没有那个文件」失败 —— 在人以为一切就绪的时候。

          Admin 的监听地址早先写在这里当前提，现在归脚本了（drop-in 里钉死，
          verify 会真的查一遍），所以只留「装了官方 Caddy」这半句。
        -->
        <ul class="checklist">
          <li>
            当前目录下有 <code>edge-node.sh</code> 与 <code>edge-agent</code> ——
            命令里那两个相对路径指的就是它们，<b>脚本不负责下载二进制</b>
          </li>
          <li>目标机器可以外连主控的 9000 端口（Agent 主动外连，无需入站放行）</li>
          <li>机器上已安装官方 Caddy（Admin 收到 127.0.0.1:2019 由脚本负责）</li>
          <li>
            命令里的 <code>--ca-pin</code> 是主控的 CA 指纹，<b>不要改也不要省</b>：
            少了它，首连就是纯 TOFU，中间人可以在那一刻把 Token 骗走。
          </li>
        </ul>

        <!--
          verify 不在 install_cmd 里，照「复制命令」做的人不会跑到它。
          而它查的两件事处置完全不同：Caddy 没起来，和 Admin 监听在了对外地址上
          —— 后者是私钥暴露（证书私钥以 load_pem 内联在运行配置里，能读 Admin
          就能读到）。不提这一步，那道检查等于不存在。
        -->
        <p class="verify">
          装完之后跑一遍 <code>sudo ./edge-node.sh verify</code>：它会真的去查
          Agent 是否接上、Caddy Admin 有没有监听在不该监听的地方。
        </p>
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
.verify {
  margin: 0;
  padding: 8px 10px;
  border-left: 2px solid var(--accent);
  background: var(--surface-sunken, var(--bg-subtle));
  font-size: var(--fs-2xs);
  color: var(--text-muted);
  line-height: 1.7;
}
.verify code {
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  color: var(--text-body);
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
