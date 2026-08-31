<script setup lang="ts">
import { ref } from 'vue'
import { parseDomainsInput } from '@/dns/targets'

/**
 * 一个顶级域名下的子域，在这里增删改。
 *
 * **改的是本地副本，「确定」才交回去** —— 弹窗里点「取消」或点遮罩关掉，
 * 父页面的编辑状态一个字不动，「保存」按钮也不会亮。半改半关留下一个
 * 亮着的保存按钮，人会存进去一份自己没看过的东西。
 */
const props = defineProps<{ domain: string; subs: string[] }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'apply', subs: string[]): void }>()

const list = ref<string[]>([...props.subs])
const addInput = ref('')

/** 跟页面上加域名一个待遇：逗号 / 空格 / 换行分隔，一次加多个。`@` 当根。 */
function addSubs(): void {
  for (const piece of parseDomainsInput(addInput.value)) {
    const sub = piece === '@' ? '' : piece
    if (!list.value.includes(sub)) list.value.push(sub)
  }
  addInput.value = ''
}

function setSub(i: number, v: string): void {
  if (i < list.value.length) list.value[i] = v
}

function removeSub(i: number): void {
  list.value.splice(i, 1)
}

function apply(): void {
  /*
   * 收尾时统一口径：去空白、`@` 归一成空串、去重。
   * 在这里做而不是边输边做 —— 人打到一半的 `www` 不该被中途改写。
   */
  const seen = new Set<string>()
  const out: string[] = []
  for (const raw of list.value) {
    const t = raw.trim()
    const sub = t === '@' ? '' : t
    if (!seen.has(sub)) {
      seen.add(sub)
      out.push(sub)
    }
  }
  emit('apply', out)
}
</script>

<template>
  <div class="mask" @click.self="emit('close')">
    <div class="modal" role="dialog" aria-modal="true" :aria-label="`管理子域 · ${domain}`">
      <header class="head">
        <div class="title">管理子域</div>
        <div class="dom mono">{{ domain }}</div>
      </header>

      <div class="body">
        <div v-if="!list.length" class="empty">
          没有子域时按「只写根记录」处理 —— 记录直接落在 <code>{{ domain }}</code> 上。
        </div>

        <div v-for="(s, i) in list" :key="i" class="sub-row">
          <input
            :value="s"
            class="text"
            placeholder="@（根）"
            aria-label="子域前缀"
            @input="setSub(i, ($event.target as HTMLInputElement).value)"
          />
          <button class="mini" type="button" title="删掉这个子域" @click="removeSub(i)">
            删除
          </button>
          <span class="preview mono">{{ s.trim() ? `${s.trim()}.${domain}` : domain }}</span>
        </div>

        <div class="add-row">
          <input
            v-model="addInput"
            class="text add-subs"
            placeholder="www, edge —— 可一次加多个，@ 表示根"
            aria-label="要添加的子域"
            @keyup.enter="addSubs"
          />
          <button class="mini add" type="button" @click="addSubs">添加</button>
        </div>

        <p class="note">记录写在 <code>子域.{{ domain }}</code> 上，<code>@</code> / 留空表示直接写在域名上。</p>
      </div>

      <footer class="foot">
        <button class="mini" type="button" @click="emit('close')">取消</button>
        <button class="primary" type="button" @click="apply">确定</button>
      </footer>
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
  width: min(560px, 100%);
  max-height: 80vh;
  display: flex;
  flex-direction: column;
  background: var(--surface-raised);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-xl);
  overflow: hidden;
}
.head {
  display: flex;
  align-items: baseline;
  gap: var(--space-3);
  padding: var(--space-4) var(--space-5);
  border-bottom: 1px solid var(--border-subtle);
}
.title {
  font-size: var(--fs-base);
  font-weight: var(--weight-bold);
  color: var(--text-strong);
}
.dom {
  font-size: var(--fs-2xs);
  color: var(--text-muted);
}
.body {
  padding: var(--space-4) var(--space-5);
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.sub-row,
.add-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
}
.sub-row .text,
.add-row .text {
  flex: 1 1 180px;
  min-width: 0;
}
.preview {
  flex: 1 0 100%;
  font-size: var(--fs-2xs);
  color: var(--text-faint);
  word-break: break-all;
}
.preview::before {
  content: '→ ';
}
.empty {
  padding: var(--space-3);
  border: 1px dashed var(--border-default);
  border-radius: var(--radius-sm);
  font-size: var(--fs-2xs);
  color: var(--text-muted);
}
.note {
  margin: 0;
  font-size: var(--fs-2xs);
  color: var(--text-muted);
}
.foot {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-2);
  padding: var(--space-3) var(--space-5);
  border-top: 1px solid var(--border-subtle);
}
</style>
