'use client'
import { LogoutOutlined, MenuFoldOutlined, MenuUnfoldOutlined, UserOutlined } from '@ant-design/icons'
import { Avatar, Button, Dropdown, Layout, Menu, Spin } from 'antd'
import Link from 'next/link'
import { usePathname, useRouter } from 'next/navigation'
import { type ReactNode, useEffect, useState } from 'react'
import { token } from '@/lib/api'
import { nav, titleOf } from '@/lib/nav'
import { BrandMark } from '@/components/BrandMark'

const { Sider, Header, Content } = Layout

/** Console shell: auth guard + sidebar + header. Every page under (console) renders inside it. */
export default function ConsoleLayout({ children }: { children: ReactNode }) {
  const pathname = usePathname()
  const router = useRouter()
  const [ready, setReady] = useState(false)
  const [collapsed, setCollapsed] = useState(false)

  useEffect(() => {
    const toLogin = () => router.replace(`/login?redirect=${encodeURIComponent(pathname)}`)
    if (!token.get()) toLogin()
    else setReady(true)
    return token.onClear(toLogin)
  }, [pathname, router])

  if (!ready)
    return (
      <div style={{ height: '100vh', display: 'grid', placeItems: 'center' }}>
        <Spin size="large" />
      </div>
    )

  const selected = nav.find((n) => (n.path === '/' ? pathname === '/' : pathname.startsWith(n.path)))?.path ?? '/'

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Sider width={216} collapsedWidth={64} collapsed={collapsed} style={{ position: 'sticky', top: 0, height: '100vh' }}>
        <div style={{ height: 60, display: 'flex', alignItems: 'center', gap: 10, padding: '0 18px', color: '#fff', fontWeight: 600, whiteSpace: 'nowrap', overflow: 'hidden' }}>
          <BrandMark size={28} inverted />
          {!collapsed && <span>环游 · 后台</span>}
        </div>
        <Menu
          theme="dark"
          mode="inline"
          selectedKeys={[selected]}
          items={nav.map((n) => ({ key: n.path, icon: n.icon, label: <Link href={n.path}>{n.title}</Link> }))}
        />
      </Sider>
      <Layout>
        <Header style={{ display: 'flex', alignItems: 'center', gap: 8, borderBottom: '1px solid #e8ebf3', position: 'sticky', top: 0, zIndex: 10 }}>
          <Button type="text" icon={collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />} onClick={() => setCollapsed(!collapsed)} />
          <span style={{ fontSize: 16, fontWeight: 600 }}>{titleOf(pathname)}</span>
          <div style={{ flex: 1 }} />
          <Dropdown
            menu={{
              items: [{ key: 'logout', icon: <LogoutOutlined />, label: '退出登录' }],
              onClick: () => token.clear(),
            }}
          >
            <span style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer', color: '#475569' }}>
              <Avatar size="small" icon={<UserOutlined />} /> 管理员
            </span>
          </Dropdown>
        </Header>
        <Content>{children}</Content>
      </Layout>
    </Layout>
  )
}
