<script setup lang="ts">
import { onErrorCaptured, ref, watch } from 'vue'
import { useRoute } from 'vue-router'

/**
 * 页面级错误边界。
 *
 * 没有它，渲染期的一个异常会让整块内容区**白屏**：没有提示、没有重试入口，
 * 界面看起来像「这一页本来就是空的」。真实踩到过一次 —— 后端把
 * `origin_rate` 给成 `null`，`toFixed` 抛异常，整个总览页消失，
 * 而顶栏和侧栏照常渲染，所以第一眼根本看不出是崩了。
 *
 * 白屏是最坏的失败方式：它既不说出了什么事，也不说该怎么办。
 */
const route = useRoute()
const error = ref<Error | null>(null)

onErrorCaptured((e) => {
  error.value = e instanceof Error ? e : new Error(String(e))
  return false // 不再往上冒泡，但仍会打到控制台供排查
})

// 切页时清掉，否则一次崩溃会把后续所有页面都挡住
watch(() => route.fullPath, () => (error.value = null))

function retry(): void {
  error.value = null
}
</script>

<template>
  <div v-if="error" class="boundary" role="alert">
    <div class="title">这一页没能渲染出来</div>
    <p class="msg">{{ error.message }}</p>
    <p class="hint">
      通常是主控返回了界面没预料到的数据。换一页仍然可用；如果反复出现，
      把这条消息发给后端。
    </p>
    <button class="retry" type="button" @click="retry">重试</button>
  </div>
  <slot v-else />
</template>

<style scoped>
.boundary {
  background: var(--surface-card);
  border: 1px solid var(--danger);
  border-radius: var(--radius-lg);
  padding: 32px var(--space-6);
  text-align: center;
}
.title {
  font-size: var(--fs-base);
  font-weight: var(--weight-bold);
  color: var(--danger-text);
  margin-bottom: var(--space-2);
}
.msg {
  margin: 0 0 var(--space-2);
  font-family: var(--font-mono);
  font-size: var(--fs-xs);
  color: var(--text-body);
  word-break: break-word;
}
.hint {
  margin: 0 auto var(--space-4);
  max-width: 46ch;
  font-size: var(--fs-2xs);
  color: var(--text-muted);
  line-height: 1.7;
}
.retry {
  padding: 6px 16px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-card);
  color: var(--text-strong);
  font-size: var(--fs-xs);
  cursor: pointer;
}
.retry:hover {
  background: var(--surface-sunken);
}
</style>
