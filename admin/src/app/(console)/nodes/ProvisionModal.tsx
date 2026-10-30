'use client'
import { CheckCircleFilled, CloseCircleFilled, EnvironmentOutlined, LoadingOutlined } from '@ant-design/icons'
import { Alert, App, Button, Form, Input, InputNumber, Modal, Segmented, Space } from 'antd'
import { useEffect, useRef, useState } from 'react'
import { api, type GeoIP, type VpnNode } from '@/lib/api'
import { errMsg, flag } from '@/lib/format'

type Phase = 'form' | 'running' | 'done' | 'failed'

/**
 * One-click node install: the operator types the server's IP and SSH password,
 * the API installs vpn-node over SSH, detects region from the IP and registers
 * the node. Keys, token and ports are all generated.
 */
export function ProvisionModal({ open, onClose, onDone }: { open: boolean; onClose: () => void; onDone: (n: VpnNode) => void }) {
  const { message } = App.useApp()
  const [form] = Form.useForm()
  const [auth, setAuth] = useState<'password' | 'key'>('password')
  const [geo, setGeo] = useState<GeoIP | null>(null)
  const [geoBusy, setGeoBusy] = useState(false)
  const [geoErr, setGeoErr] = useState<string>()
  const [phase, setPhase] = useState<Phase>('form')
  const [log, setLog] = useState('')
  const [error, setError] = useState<string>()
  const [node, setNode] = useState<VpnNode>()
  const logRef = useRef<HTMLPreElement>(null)

  useEffect(() => {
    if (open) {
      form.resetFields()
      form.setFieldsValue({ port: 22, user: 'root', server_name: 'www.apple.com' })
      setAuth('password')
      setGeo(null)
      setGeoErr(undefined)
      setPhase('form')
      setLog('')
      setError(undefined)
      setNode(undefined)
    }
  }, [open, form])

  useEffect(() => {
    logRef.current?.scrollTo({ top: logRef.current.scrollHeight })
  }, [log])

  const lookup = async () => {
    const host = (form.getFieldValue('host') as string | undefined)?.trim()
    if (!host || host === geo?.ip) return
    setGeoBusy(true)
    setGeoErr(undefined)
    try {
      const g = await api.nodes.geoip(host)
      setGeo(g)
      form.setFieldsValue({ name: g.suggested_name, region: g.country_code })
    } catch (e) {
      setGeo(null)
      setGeoErr(errMsg(e))
    } finally {
      setGeoBusy(false)
    }
  }

  const start = async () => {
    const v = await form.validateFields()
    setPhase('running')
    setLog('')
    setError(undefined)
    try {
      await api.nodes.provision(
        {
          host: v.host.trim(),
          port: v.port,
          user: v.user,
          password: auth === 'password' ? v.password : undefined,
          private_key: auth === 'key' ? v.private_key : undefined,
          name: v.name?.trim(),
          region: v.region?.trim(),
          server_name: v.server_name?.trim(),
        },
        (e) => {
          if (e.type === 'log') setLog((l) => l + e.text)
          else if (e.type === 'error') {
            setError(e.message)
            setPhase('failed')
          } else {
            setNode(e.node)
            setPhase('done')
            onDone(e.node)
            message.success('节点已上线')
          }
        },
      )
      setPhase((p) => (p === 'running' ? 'failed' : p))
      setError((er) => er ?? undefined)
    } catch (e) {
      setError(errMsg(e))
      setPhase('failed')
    }
  }

  const footer =
    phase === 'form' ? (
      <Space>
        <Button onClick={onClose}>取消</Button>
        <Button type="primary" onClick={start}>开始安装</Button>
      </Space>
    ) : phase === 'running' ? (
      <Button disabled>安装中…</Button>
    ) : phase === 'failed' ? (
      <Space>
        <Button onClick={onClose}>关闭</Button>
        <Button type="primary" onClick={() => setPhase('form')}>修改后重试</Button>
      </Space>
    ) : (
      <Button type="primary" onClick={onClose}>完成</Button>
    )

  return (
    <Modal
      open={open}
      onCancel={phase === 'running' ? undefined : onClose}
      closable={phase !== 'running'}
      maskClosable={false}
      width={640}
      title="一键安装节点"
      footer={footer}
      destroyOnHidden
    >
      {phase === 'form' ? (
        <Form form={form} layout="vertical" requiredMark={false}>
          <p className="muted" style={{ marginTop: -4 }}>
            填一台新服务器（Debian / Ubuntu）的 IP 和 SSH 登录信息，后台会自动安装、生成密钥、识别地区并上线。
          </p>
          <div style={{ display: 'flex', gap: 12 }}>
            <Form.Item name="host" label="服务器 IP" rules={[{ required: true, message: '请填写 IP 或域名' }]} style={{ flex: 1 }}>
              <Input placeholder="如 203.0.113.10" onBlur={lookup} onPressEnter={lookup} autoFocus />
            </Form.Item>
            <Form.Item name="port" label="SSH 端口" style={{ width: 110 }}>
              <InputNumber min={1} max={65535} style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="user" label="用户" style={{ width: 120 }}>
              <Input />
            </Form.Item>
          </div>

          <div className="geo-line">
            {geoBusy ? (
              <span className="muted"><LoadingOutlined /> 正在识别地区…</span>
            ) : geo ? (
              <span>
                <EnvironmentOutlined /> {flag(geo.country_code)} {geo.country}
                {geo.region ? ` · ${geo.region}` : ''}
                {geo.city ? ` · ${geo.city}` : ''}
              </span>
            ) : geoErr ? (
              <span className="muted">地区识别失败（{geoErr}），可手动填写</span>
            ) : (
              <span className="muted">输入 IP 后自动识别地区</span>
            )}
          </div>

          <Form.Item label="登录方式" style={{ marginBottom: 12 }}>
            <Segmented
              value={auth}
              onChange={(v) => setAuth(v as 'password' | 'key')}
              options={[
                { label: '密码', value: 'password' },
                { label: '私钥', value: 'key' },
              ]}
            />
          </Form.Item>
          {auth === 'password' ? (
            <Form.Item name="password" label="SSH 密码" rules={[{ required: true, message: '请填写密码' }]}>
              <Input.Password autoComplete="new-password" />
            </Form.Item>
          ) : (
            <Form.Item name="private_key" label="SSH 私钥" rules={[{ required: true, message: '请粘贴私钥' }]}>
              <Input.TextArea rows={4} className="mono" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" />
            </Form.Item>
          )}

          <div style={{ display: 'flex', gap: 12 }}>
            <Form.Item name="name" label="节点名称" style={{ flex: 1 }} extra="留空按地区自动命名">
              <Input placeholder="自动" />
            </Form.Item>
            <Form.Item name="region" label="地区代码" style={{ width: 120 }} normalize={(v: string) => v?.toUpperCase()}>
              <Input maxLength={2} placeholder="自动" />
            </Form.Item>
          </div>
          <Form.Item name="server_name" label="伪装域名" extra="一般不用改。需要境外、TLS1.3、经典 X25519 的站点。">
            <Input />
          </Form.Item>
          <Alert type="info" showIcon message="SSH 密码只用于本次安装，不会保存。安装会占用服务器的 443 端口。" />
        </Form>
      ) : (
        <>
          <div className="provision-status">
            {phase === 'running' && <><LoadingOutlined /> 正在安装，通常需要 1 分钟左右，请勿关闭…</>}
            {phase === 'done' && (
              <><CheckCircleFilled style={{ color: '#12A150' }} /> {flag(node?.region ?? '')} {node?.name} 已安装并上线，App 中已可选择。</>
            )}
            {phase === 'failed' && <><CloseCircleFilled style={{ color: '#D64545' }} /> 安装失败：{error}</>}
          </div>
          <pre ref={logRef} className="provision-log">{log || '等待服务器响应…'}</pre>
        </>
      )}
    </Modal>
  )
}
