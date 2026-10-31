'use client'
import { RentalFields } from '@/components/ExpiryField'
import { CheckCircleFilled, CloseCircleFilled, EnvironmentOutlined, LoadingOutlined, WarningFilled } from '@ant-design/icons'
import { Alert, App, Button, Form, Input, InputNumber, Modal, Segmented, Space, Tag, Typography } from 'antd'
import { useEffect, useRef, useState } from 'react'
import { api, type GeoIP, type ProvisionReport, type VpnNode } from '@/lib/api'
import { bytes, errMsg, flag } from '@/lib/format'

type Phase = 'form' | 'checking' | 'running' | 'done' | 'failed'

/**
 * One-click node install. The operator types the server IP + SSH login; the API
 * first runs a read-only environment check (arch / systemd / free port / disk),
 * then installs vpn-node over SSH, generating keys, token and detecting region.
 */
export function ProvisionModal({ open, onClose, onDone }: { open: boolean; onClose: () => void; onDone: (n: VpnNode) => void }) {
  const { message } = App.useApp()
  const [form] = Form.useForm()
  const [auth, setAuth] = useState<'password' | 'key'>('password')
  const [geo, setGeo] = useState<GeoIP | null>(null)
  const [geoBusy, setGeoBusy] = useState(false)
  const [geoErr, setGeoErr] = useState<string>()
  const [phase, setPhase] = useState<Phase>('form')
  const [report, setReport] = useState<ProvisionReport>()
  const [log, setLog] = useState('')
  const [error, setError] = useState<string>()
  const [node, setNode] = useState<VpnNode>()
  const logRef = useRef<HTMLPreElement>(null)

  useEffect(() => {
    if (open) {
      form.resetFields()
      form.setFieldsValue({ port: 22, user: 'root', node_port: 443, server_name: 'www.apple.com' })
      setAuth('password')
      setGeo(null)
      setGeoErr(undefined)
      setPhase('form')
      setReport(undefined)
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

  const reqBody = (v: Record<string, unknown>, withHostKey: boolean) => ({
    host: (v.host as string).trim(),
    port: v.port as number,
    user: v.user as string,
    password: auth === 'password' ? (v.password as string) : undefined,
    private_key: auth === 'key' ? (v.private_key as string) : undefined,
    node_port: v.node_port as number,
    name: (v.name as string)?.trim(),
    region: (v.region as string)?.trim(),
    expires_at: (v.expires_at as string) ?? undefined,
    vendor: (v.vendor as string)?.trim(),
    vendor_url: (v.vendor_url as string)?.trim(),
    server_name: (v.server_name as string)?.trim(),
    // The install only talks to the server the check saw (same SSH host key);
    // the check itself is what shows the key.
    host_key: withHostKey ? report?.host_key || undefined : undefined,
  })

  // Step 1: read-only environment check.
  const check = async () => {
    const v = await form.validateFields()
    setPhase('checking')
    setReport(undefined)
    try {
      const rep = await api.nodes.precheck(reqBody(v, false))
      setReport(rep)
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setPhase('form')
    }
  }

  const applySuggestedPort = () => {
    if (report?.suggest_port) {
      form.setFieldValue('node_port', report.suggest_port)
      setReport(undefined)
      message.info(`已改用端口 ${report.suggest_port}，请重新检测`)
    }
  }

  // Step 2: install (re-checks server-side; this streams a live log).
  const install = async () => {
    const v = await form.validateFields()
    setPhase('running')
    setLog('')
    setError(undefined)
    try {
      await api.nodes.provision(reqBody(v, true), (e) => {
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
      })
      setPhase((p) => (p === 'running' ? 'failed' : p))
    } catch (e) {
      setError(errMsg(e))
      setPhase('failed')
    }
  }

  const checkedOK = report && (report.fatal?.length ?? 0) === 0
  const footer =
    phase === 'running' ? (
      <Button disabled>安装中…</Button>
    ) : phase === 'done' ? (
      <Button type="primary" onClick={onClose}>完成</Button>
    ) : phase === 'failed' ? (
      <Space>
        <Button onClick={onClose}>关闭</Button>
        <Button type="primary" onClick={() => { setPhase('form'); setReport(undefined) }}>修改后重试</Button>
      </Space>
    ) : (
      <Space>
        <Button onClick={onClose}>取消</Button>
        <Button loading={phase === 'checking'} onClick={check}>检测环境</Button>
        <Button type="primary" disabled={!checkedOK} onClick={install}>开始安装</Button>
      </Space>
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
      {phase === 'running' || phase === 'done' || phase === 'failed' ? (
        <>
          <div className="provision-status">
            {phase === 'running' && <><LoadingOutlined /> 正在安装，通常 1 分钟左右，请勿关闭…</>}
            {phase === 'done' && (
              <><CheckCircleFilled style={{ color: '#12A150' }} /> {flag(node?.region ?? '')} {node?.name} 已安装并上线，App 中已可选择。</>
            )}
            {phase === 'failed' && <><CloseCircleFilled style={{ color: '#D64545' }} /> 安装失败：{error}</>}
          </div>
          <pre ref={logRef} className="provision-log">{log || '等待服务器响应…'}</pre>
        </>
      ) : (
        <Form
          form={form}
          layout="vertical"
          requiredMark={false}
          // Another address is another server: its check (and SSH host key) is still to come.
          onValuesChange={(changed) => {
            if ('host' in changed || 'port' in changed) setReport(undefined)
          }}
        >
          <p className="muted" style={{ marginTop: -4 }}>
            填一台新服务器（Debian / Ubuntu）的 IP 和 SSH 登录信息，先「检测环境」确认可用，再「开始安装」。密钥、Token、端口都会自动处理。
          </p>
          <div style={{ display: 'flex', gap: 12 }}>
            <Form.Item name="host" label="服务器 IP" rules={[{ required: true, message: '请填写 IP 或域名' }]} style={{ flex: 1 }}>
              <Input placeholder="如 203.0.113.10" onBlur={lookup} onPressEnter={lookup} autoFocus />
            </Form.Item>
            <Form.Item name="port" label="SSH 端口" style={{ width: 100 }}>
              <InputNumber min={1} max={65535} style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="user" label="用户" style={{ width: 100 }}>
              <Input />
            </Form.Item>
          </div>

          <div className="geo-line">
            {geoBusy ? (
              <span className="muted"><LoadingOutlined /> 正在识别地区…</span>
            ) : geo ? (
              <span><EnvironmentOutlined /> {flag(geo.country_code)} {geo.country}{geo.region ? ` · ${geo.region}` : ''}{geo.city ? ` · ${geo.city}` : ''}</span>
            ) : geoErr ? (
              <span className="muted">地区识别失败（{geoErr}），可手动填写</span>
            ) : (
              <span className="muted">输入 IP 后自动识别地区</span>
            )}
          </div>

          <Form.Item label="登录方式" style={{ marginBottom: 12 }}>
            <Segmented value={auth} onChange={(v) => setAuth(v as 'password' | 'key')}
              options={[{ label: '密码', value: 'password' }, { label: '私钥', value: 'key' }]} />
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
            <Form.Item name="region" label="地区" style={{ width: 90 }} normalize={(v: string) => v?.toUpperCase()}>
              <Input maxLength={2} placeholder="自动" />
            </Form.Item>
            <Form.Item name="node_port" label="节点端口" style={{ width: 110 }} tooltip="节点对外监听端口，默认 443；被占用时检测会建议一个空闲端口">
              <InputNumber min={1} max={65535} style={{ width: '100%' }} />
            </Form.Item>
          </div>
          <Form.Item name="server_name" label="伪装域名" extra="一般不用改。需要境外、TLS1.3、经典 X25519 的站点。">
            <Input />
          </Form.Item>
          <RentalFields />

          {phase === 'checking' && <Alert type="info" showIcon icon={<LoadingOutlined />} message="正在通过 SSH 检测服务器环境…" />}
          {report && <ReportPanel report={report} onApplyPort={applySuggestedPort} />}
          {!report && phase !== 'checking' && (
            <Alert type="info" showIcon message="SSH 密码只用于本次安装，不会保存。请先点「检测环境」。" />
          )}
        </Form>
      )}
    </Modal>
  )
}

function ReportPanel({ report: r, onApplyPort }: { report: ProvisionReport; onApplyPort: () => void }) {
  const ok = (r.fatal?.length ?? 0) === 0
  const row = (label: string, good: boolean, text: string) => (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '3px 0' }}>
      {good ? <CheckCircleFilled style={{ color: '#12A150' }} /> : <CloseCircleFilled style={{ color: '#D64545' }} />}
      <span style={{ width: 72, color: '#6E6F73' }}>{label}</span>
      <span>{text}</span>
    </div>
  )
  return (
    <div style={{ border: '1px solid #e8e6e0', borderRadius: 10, padding: '12px 14px' }}>
      <div style={{ marginBottom: 8, fontWeight: 600 }}>
        环境检测{' '}
        {ok ? <Tag color="success">可以安装</Tag> : <Tag color="error">需要处理</Tag>}
      </div>
      {row('系统', !!r.os, `${r.os || '未知'} · ${r.arch || '?'}${r.virt && r.virt !== 'none' ? ` · ${r.virt}` : ''}`)}
      {row('架构', r.arch_ok, r.arch_ok ? `有匹配的 ${r.arch} 程序` : `缺少 ${r.arch} 程序`)}
      {row('systemd', r.systemd, r.systemd ? '支持' : '不支持')}
      {row('端口', r.port_free, r.port_free ? `${r.port} 空闲` : `${r.port} 被 ${r.port_user || '占用'}`)}
      {row('资源', r.disk_free_gb >= 1, `内存 ${r.ram_mb} MB · 磁盘空闲 ${r.disk_free_gb} GB`)}
      {row('时间同步', r.time_synced, r.time_synced ? '已同步' : '未同步（安装时尝试开启）')}
      {r.host_key && (
        <div style={{ padding: '3px 0', color: '#6E6F73', fontSize: 12 }}>
          SSH 主机密钥 <Typography.Text code copyable style={{ fontSize: 12 }}>{r.host_key}</Typography.Text>
          <span>（可在服务商控制台核对；安装和以后的升级只连接这台服务器）</span>
        </div>
      )}
      {r.fatal?.map((f) => (
        <Alert key={f} type="error" showIcon style={{ marginTop: 8 }} message={f}
          action={!r.port_free && r.suggest_port && f.includes('端口') ? <Button size="small" onClick={onApplyPort}>改用 {r.suggest_port}</Button> : undefined} />
      ))}
      {r.warn?.map((wm) => <Alert key={wm} type="warning" showIcon icon={<WarningFilled />} style={{ marginTop: 8 }} message={wm} />)}
      {ok && <div style={{ marginTop: 8, color: '#12A150' }}>环境正常，可以点「开始安装」。</div>}
    </div>
  )
}
