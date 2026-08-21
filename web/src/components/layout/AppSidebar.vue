<script setup lang="ts">
import { useRoute } from 'vue-router'
import { NAV, NAV_GROUPS } from '@/router/nav'

defineProps<{ counts: Record<string, number | string> }>()

const route = useRoute()
const isActive = (path: string) => route.path.startsWith(path)
</script>

<template>
  <nav class="sidebar">
    <div class="brand">
      <div class="mark">EC</div>
      <div class="brand-text">
        <div class="name">Edge Controller</div>
        <div class="env">prod · master-hk</div>
      </div>
    </div>

    <template v-for="group in NAV_GROUPS" :key="group">
      <div class="group">{{ group }}</div>
      <RouterLink
        v-for="item in NAV.filter((n) => n.group === group)"
        :key="item.key"
        :to="item.path"
        class="item"
        :class="{ active: isActive(item.path) }"
      >
        <svg
          width="16"
          height="16"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          aria-hidden="true"
        >
          <path :d="item.icon" />
        </svg>
        <span class="label">{{ item.label }}</span>
        <span v-if="counts[item.key] !== undefined && counts[item.key] !== ''" class="count">
          {{ counts[item.key] }}
        </span>
      </RouterLink>
    </template>

    <div class="foot">
      <div class="platform">REALTIME EDGE PLATFORM</div>
      <div class="stack">Master · Agent · Caddy</div>
    </div>
  </nav>
</template>

<style scoped>
.sidebar {
  width: 226px;
  flex: none;
  background: var(--surface-card);
  border-right: 1px solid var(--border-subtle);
  padding: var(--space-4) var(--space-3) var(--space-5);
  display: flex;
  flex-direction: column;
  gap: var(--space-0-5);
  position: sticky;
  top: 0;
  height: 100vh;
  overflow: auto;
}
.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 2px var(--space-2) 18px;
}
.mark {
  width: 28px;
  height: 28px;
  flex: none;
  border-radius: var(--radius-sm);
  background: linear-gradient(135deg, var(--azure-500), var(--cyan-400));
  display: grid;
  place-items: center;
  color: var(--text-on-accent);
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  font-weight: var(--weight-bold);
  box-shadow: var(--shadow-xs);
}
.brand-text {
  min-width: 0;
}
.name {
  font-family: var(--font-display);
  font-size: var(--fs-sm);
  font-weight: var(--weight-bold);
  letter-spacing: var(--tracking-tight);
  color: var(--text-strong);
}
.env {
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  color: var(--text-muted);
}
.group {
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-muted);
  font-weight: var(--weight-semibold);
  padding: 14px 10px var(--space-1);
}
.group:first-of-type {
  padding-top: var(--space-1-5);
}
.item {
  display: flex;
  align-items: center;
  gap: 9px;
  width: 100%;
  text-align: left;
  padding: 7px 10px;
  border: 0;
  border-radius: var(--radius-md);
  cursor: pointer;
  font-size: var(--fs-sm);
  background: transparent;
  color: var(--text-body);
  text-decoration: none;
  transition: var(--transition-colors);
}
.item:hover {
  background: var(--surface-sunken);
  color: var(--text-body);
}
.item.active {
  background: var(--accent-subtle);
  color: var(--accent-text);
  font-weight: var(--weight-semibold);
}
.label {
  flex: 1;
}
.count {
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  color: var(--text-faint);
}
.item.active .count {
  color: var(--accent-text);
}
.foot {
  margin-top: auto;
  padding: var(--space-4) 10px 0;
  border-top: 1px solid var(--border-subtle);
}
.platform {
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  letter-spacing: var(--tracking-caps);
  color: var(--text-faint);
}
.stack {
  font-size: var(--fs-2xs);
  color: var(--text-muted);
  margin-top: 2px;
}
</style>
