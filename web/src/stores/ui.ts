import { defineStore } from 'pinia'
import { ref, watch } from 'vue'

export type Theme = 'light' | 'dark'
export type ToastKind = 'ok' | 'info' | 'warn' | 'danger'

export interface Toast {
  id: number
  kind: ToastKind
  title: string
  detail: string
}

const THEME_KEY = 'ec.theme'
const TOAST_MS = 4_200

function initialTheme(): Theme {
  try {
    const saved = localStorage.getItem(THEME_KEY)
    if (saved === 'light' || saved === 'dark') return saved
  } catch {
    // 隐私模式 / 禁用站点数据时读会抛，按跟随系统处理
  }
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

export const useUiStore = defineStore('ui', () => {
  const theme = ref<Theme>(initialTheme())
  const toasts = ref<Toast[]>([])
  const paletteOpen = ref(false)

  function applyTheme(): void {
    if (theme.value === 'dark') document.documentElement.setAttribute('data-theme', 'dark')
    else document.documentElement.removeAttribute('data-theme')
  }

  watch(
    theme,
    (t) => {
      applyTheme()
      try {
        localStorage.setItem(THEME_KEY, t)
      } catch {
        // 存不了就算了，本次会话内仍然生效
      }
    },
    { immediate: true },
  )

  function toggleTheme(): void {
    theme.value = theme.value === 'dark' ? 'light' : 'dark'
  }

  let toastSeq = 0
  function toast(kind: ToastKind, title: string, detail = ''): void {
    const id = ++toastSeq
    toasts.value = [...toasts.value, { id, kind, title, detail }]
    setTimeout(() => dismiss(id), TOAST_MS)
  }

  function dismiss(id: number): void {
    toasts.value = toasts.value.filter((t) => t.id !== id)
  }

  return { theme, toasts, paletteOpen, toggleTheme, toast, dismiss }
})
