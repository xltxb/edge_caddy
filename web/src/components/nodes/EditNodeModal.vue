<script setup lang="ts">
import { ref } from 'vue'
import { errorText } from '@/api/http'
import { useNodesStore } from '@/stores/nodes'
import type { EdgeNode } from '@/model'

const props = defineProps<{ node: EdgeNode }>()
const emit = defineEmits<{ (e: 'close'): void }>()
const nodes = useNodesStore()

/**
 * 四项都回填当前值 —— 契约 §4 要求四项必填，只想改城市也得把其余三项一起发。
 *
 * 回填而不是留空：留空的话人改完城市一提交，vendor/line/public_ip 会被三个空串
 * 覆盖掉，而这个接口没有「省略 = 保持不变」的语义。
 */
const form = ref({
  city: props.node.city,
  vendor: props.node.vendor,
  line: props.node.line,
  public_ip: props.node.ip,
})

const busy = ref(false)
const error = ref('')
/** 保存后停在结果态，由人自己关掉 —— detail 要人看见，不能自己走掉。 */
const done = ref<{ synced: boolean; detail: string } | null>(null)

async function submit(): Promise<void> {
  busy.value = true
  error.value = ''
  try {
    const r = await nodes.updateNode(props.node.id, { ...form.value })
    /*
     * **判据是 detail 空不空，不是前端自己比 IP 改没改。**
     *
     * 契约 §4：只改城市/机房/线路时 `dns_synced: false` 且 `detail` 为空串——
     * 那不是失败，是这次改动跟解析无关。改了 IP 时 detail 一定有话说。
     *
     * 前端比字符串会在 IPv6 上翻车：`2001:db8::1` 和 `2001:0db8:0:0:0:0:0:1`
     * 是同一个地址而字符串不同。后端按 IP 的值比（net.IP.Equal），前端按字符串
     * 比，两边会对同一次提交得出不同结论 —— 于是界面报「IP 已改而解析没变动」
     * 这样一句假警报。**判据要跟做决定的那一方同源。**
     */
    done.value = { synced: r.dns_synced, detail: r.detail }
  } catch (e) {
    error.value = errorText(e, '保存失败')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="mask" @click.self="emit('close')">
    <div class="modal" role="dialog" aria-modal="true" aria-label="修改节点">
      <div class="title">修改 {{ node.id }}</div>

      <template v-if="!done">
        <!--
          节点 ID 不在表单里，而且要说明为什么 —— 一个只是「不能改」的灰输入框
          会让人以为是权限问题，然后去找能改它的人。
        -->
        <p class="lead">
          节点 ID 是这台机器的身份，写在隧道证书里，改不了 ——
          换一台机器是「删掉再接一台」。<b>状态、解析开关、下线标记也不在这里改</b>，
          它们各有自己的入口。
        </p>
        <form class="form" @submit.prevent="submit">
          <label><span>城市</span><input v-model="form.city" required /></label>
          <label><span>服务商</span><input v-model="form.vendor" required /></label>
          <label><span>中转线路</span><input v-model="form.line" required /></label>
          <label>
            <span>公网 IP</span>
            <input v-model="form.public_ip" required />
          </label>
          <!--
            这句是**改之前**说的。改完再说「解析已同步」是通知，而人需要的是在
            按下去之前就知道这一下会动到线上流量。
          -->
          <p class="hint">
            改公网 IP 会立刻把解析推到服务商 —— 它会被写进 DNS 记录。
            写错的值不会在这里报错，而是在下一次同步后把访问者送到一个不存在的地方。
          </p>
          <p v-if="error" class="err">{{ error }}</p>
          <div class="actions">
            <button class="ghost" type="button" @click="emit('close')">取消</button>
            <button class="primary" type="submit" :disabled="busy">
              {{ busy ? '保存中…' : '保存' }}
            </button>
          </div>
        </form>
      </template>

      <!--
        结果态。**detail 原样显示**，三种情况说三句不同的话：

        - synced：解析真的变了
        - !synced 且 detail 空：这次改动跟解析无关 —— **不是失败**，不能用警示色
        - !synced 且 detail 有话：解析该变而没变成，那是要人接着处理的事
      -->
      <template v-else>
        <p v-if="done.synced" class="result ok">
          已保存，解析已同步到服务商。
        </p>
        <p v-else-if="!done.detail" class="result ok">
          已保存。这次改动不涉及公网 IP，解析没有变动。
        </p>
        <p v-else class="result bad">
          已保存，<b>但解析没有变动</b> —— 库里的 IP 已经改了，而访问者仍会被送到旧地址。
        </p>
        <p v-if="done.detail" class="detail">{{ done.detail }}</p>
        <div class="actions">
          <button class="primary" type="button" @click="emit('close')">知道了</button>
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
  width: min(460px, 100%);
  background: var(--surface-raised);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-xl);
  padding: var(--space-5);
  max-height: 86vh;
  overflow-y: auto;
}
.title {
  font-family: var(--font-mono);
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
.lead b {
  color: var(--text-body);
  font-weight: var(--weight-semibold);
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
  outline: none;
}
.hint {
  margin: 0;
  font-size: var(--fs-2xs);
  color: var(--text-muted);
  line-height: 1.7;
}
.result {
  margin: 0 0 var(--space-2);
  font-size: var(--fs-xs);
  line-height: 1.7;
  color: var(--text-body);
}
.result.bad {
  color: var(--danger-text);
}
.result b {
  font-weight: var(--weight-semibold);
}
.detail {
  margin: 0 0 var(--space-4);
  padding: var(--space-3);
  border-radius: var(--radius-sm);
  background: var(--surface-sunken);
  border: 1px solid var(--border-subtle);
  font-size: var(--fs-2xs);
  line-height: 1.7;
  color: var(--text-body);
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
