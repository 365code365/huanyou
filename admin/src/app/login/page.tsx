'use client'
import { KeyOutlined } from '@ant-design/icons'
import { App, Button, Form, Input } from 'antd'
import { useRouter } from 'next/navigation'
import { useState } from 'react'
import { api, token } from '@/lib/api'
import { errMsg } from '@/lib/format'
import { BrandMark } from '@/components/BrandMark'
import { colors } from '@/lib/theme'

export default function LoginPage() {
  const router = useRouter()
  const { message } = App.useApp()
  const [loading, setLoading] = useState(false)

  const submit = async ({ password }: { password: string }) => {
    setLoading(true)
    try {
      const { token: t } = await api.login(password)
      token.set(t)
      const redirect = new URLSearchParams(window.location.search).get('redirect')
      router.replace(redirect && redirect.startsWith('/') && !redirect.startsWith('//') ? redirect : '/')
    } catch (e) {
      message.error(errMsg(e))
      setLoading(false)
    }
  }

  return (
    <div
      style={{
        minHeight: '100vh',
        display: 'grid',
        placeItems: 'center',
        background: colors.bg,
      }}
    >
      <div style={{ width: 360, background: '#fff', borderRadius: 18, padding: '36px 32px', boxShadow: '0 20px 60px rgba(15,23,42,.08)', textAlign: 'center' }}>
        <div style={{ width: 56, margin: '0 auto 14px' }}>
          <BrandMark size={56} />
        </div>
        <h1 style={{ fontSize: 20, margin: '0 0 4px' }}>环游 · 管理后台</h1>
        <p className="muted" style={{ margin: '0 0 22px' }}>
          请输入管理员密码
        </p>
        <Form onFinish={submit}>
          <Form.Item name="password" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password size="large" prefix={<KeyOutlined />} placeholder="管理员密码" autoFocus />
          </Form.Item>
          <Button type="primary" htmlType="submit" size="large" block loading={loading}>
            登录
          </Button>
        </Form>
      </div>
    </div>
  )
}
