/**
 * 左导航结构 —— 与设计稿一一对应：三个分组、11 个一级入口。
 *
 * 每项的 `count` 是一个取数函数而不是常量，因为设计稿里这些数字是活的
 * （工作台显示待下发项数、DNS 显示参与解析的节点数）。计数由各页面的 store
 * 供给，这里只声明「取哪个数」。
 */

export type NavGroup = '调度' | '配置' | '运维'

export interface NavItem {
  key: string
  label: string
  path: string
  group: NavGroup
  /** 徽标图标的 SVG path 集合，与设计稿的 lucide 风格线性图标一致。 */
  icon: string
}

export const NAV: NavItem[] = [
  {
    key: 'overview',
    label: '集群总览',
    path: '/overview',
    group: '调度',
    icon: 'M3 3h7v9H3zM14 3h7v5h-7zM14 12h7v9h-7zM3 16h7v5H3z',
  },
  {
    key: 'nodes',
    label: '边缘节点',
    path: '/nodes',
    group: '调度',
    icon: 'M2 2h20v8H2zM2 14h20v8H2zM6 6h.01M6 18h.01',
  },
  {
    key: 'dns',
    label: 'DNS 调度',
    path: '/dns',
    group: '调度',
    icon: 'M12 2a10 10 0 1 0 0 20 10 10 0 0 0 0-20M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20M2 12h20',
  },
  {
    key: 'workbench',
    label: '配置工作台',
    path: '/workbench',
    group: '配置',
    icon: 'M21 4h-7M10 4H3M21 12h-9M8 12H3M21 20h-5M12 20H3M14 2v4M8 10v4M16 18v4',
  },
  {
    key: 'routes',
    label: '反代路由',
    path: '/routes',
    group: '配置',
    icon: 'M4 4h6v6H4zM14 14h6v6h-6zM10 7h4a3 3 0 0 1 3 3v4',
  },
  {
    key: 'acl',
    label: '访问控制',
    path: '/acl',
    group: '配置',
    icon: 'M12 2 4 6v6c0 5 3.4 8.9 8 10 4.6-1.1 8-5 8-10V6z',
  },
  {
    key: 'certs',
    label: '证书',
    path: '/certs',
    group: '配置',
    icon: 'M12 15a4 4 0 1 0 0-8 4 4 0 0 0 0 8M8.2 13.8 7 22l5-3 5 3-1.2-8.2',
  },
  {
    key: 'deploys',
    label: '下发记录',
    path: '/deploys',
    group: '运维',
    icon: 'M12 3v12M7 10l5 5 5-5M4 19h16',
  },
  {
    key: 'audit',
    label: '审计日志',
    path: '/audit',
    group: '运维',
    icon: 'M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8zM14 2v6h6M9 13h6M9 17h4',
  },
  {
    key: 'alerts',
    label: '告警通知',
    path: '/alerts',
    group: '运维',
    icon: 'M18 8a6 6 0 1 0-12 0c0 7-3 9-3 9h18s-3-2-3-9M13.7 21a2 2 0 0 1-3.4 0',
  },
  {
    key: 'settings',
    label: '系统设置',
    path: '/settings',
    group: '运维',
    icon: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-2.9 1.2V21a2 2 0 1 1-4 0v-.1A1.7 1.7 0 0 0 7 19.4a1.7 1.7 0 0 0-1.9.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0-1.2-2.9H1a2 2 0 1 1 0-4h.1A1.7 1.7 0 0 0 2.6 7a1.7 1.7 0 0 0-.3-1.9l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.9.3H7a1.7 1.7 0 0 0 1-1.5V1a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.9-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.9V7a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z',
  },
]

export const NAV_GROUPS: NavGroup[] = ['调度', '配置', '运维']
