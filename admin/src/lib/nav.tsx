import {
  AppstoreOutlined,
  ApiOutlined,
  BellOutlined,
  CloudUploadOutlined,
  DatabaseOutlined,
  CustomerServiceOutlined,
  DashboardOutlined,
  ProfileOutlined,
  TranslationOutlined,
  SettingOutlined,
  TagsOutlined,
  UserOutlined,
} from '@ant-design/icons'
import type { ReactNode } from 'react'

/**
 * Sidebar menu. To add a page: create src/app/(console)/<path>/page.tsx and
 * add an entry here. `path` is relative to the /admin base path.
 */
export const nav: { path: string; title: string; icon: ReactNode }[] = [
  { path: '/', title: '概览', icon: <DashboardOutlined /> },
  { path: '/users', title: '用户', icon: <UserOutlined /> },
  { path: '/orders', title: '订单', icon: <ProfileOutlined /> },
  { path: '/plans', title: '套餐', icon: <TagsOutlined /> },
  { path: '/nodes', title: '节点', icon: <ApiOutlined /> },
  { path: '/monitor', title: '监控预警', icon: <BellOutlined /> },
  { path: '/apps', title: '推荐应用', icon: <AppstoreOutlined /> },
  { path: '/contacts', title: '客服与社交', icon: <CustomerServiceOutlined /> },
  { path: '/releases', title: '版本发布', icon: <CloudUploadOutlined /> },
  { path: '/i18n', title: '多语言', icon: <TranslationOutlined /> },
  { path: '/system', title: '备份与克隆', icon: <DatabaseOutlined /> },
  { path: '/settings', title: '系统设置', icon: <SettingOutlined /> },
]

export const titleOf = (pathname: string) =>
  nav.find((n) => (n.path === '/' ? pathname === '/' : pathname.startsWith(n.path)))?.title ?? ''
