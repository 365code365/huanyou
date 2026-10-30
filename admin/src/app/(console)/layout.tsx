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
  // Remembered across reloads; the sider also collapses itself on narrow screens.
  const [collapsed, setCollapsedState] = useState(() => typeof window !== 'undefined' && localStorage.getItem('admin_sider_collapsed') === '1')
  const setCollapsed = (v: boolean) => {
    setCollapsedState(v)
    localStorage.setItem('admin_sider_collapsed', v ? '1' : '0')
  }

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
    // Sidebar and content scroll independently: the outer shell is exactly one
    // viewport tall and each column has its own scroll area.
    <Layout style={{ height: '100vh', overflow: 'hidden' }}>
      <Sider
        width={216}
        collapsedWidth={64}
        collapsed={collapsed}
        breakpoint="lg"
        onBreakpoint={(narrow) => narrow && setCollapsedState(true)}
        style={{ height: '100vh', overflowY: 'auto', overflowX: 'hidden' }}
        className="console-sider"
      >
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
      <Layout style={{ height: '100vh', overflow: 'hidden' }}>
        <Header style={{ display: 'flex', alignItems: 'center', gap: 8, borderBottom: '1px solid #e8ebf3', flex: 'none', zIndex: 10 }}>
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
        <Content id="console-content" style={{ overflowY: 'auto', flex: 1, minHeight: 0 }}>{children}</Content>
      </Layout>
    </Layout>
  )
}
