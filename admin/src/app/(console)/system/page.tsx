'use client'
import { CheckCircleFilled, CloudDownloadOutlined, CloudServerOutlined, CloseCircleFilled, InboxOutlined, LoadingOutlined, RollbackOutlined } from '@ant-design/icons'
import { Alert, App, Button, Checkbox, Col, Descriptions, Form, Input, InputNumber, Progress, Row, Segmented, Space, Switch, Upload } from 'antd'
import type { UploadFile } from 'antd'
import { useEffect, useRef, useState } from 'react'
import { PageCard } from '@/components/PageCard'
import { useAsync } from '@/hooks/useAsync'
import { api } from '@/lib/api'
import { bytes, errMsg } from '@/lib/format'

export default function SystemPage() {
  return (
    <div className="page">
      <Row gutter={16}>
        <Col xs={24} xl={12}>
          <BackupCard />
          <div style={{ height: 16 }} />
          <RestoreCard />
        </Col>
        <Col xs={24} xl={12}>
          <CloneCard />
        </Col>
      </Row>
    </div>
  )
}

function BackupCard() {
  const { message } = App.useApp()
  const { data } = useAsync(api.system.backupInfo)
  const [pw, setPw] = useState('')
  const [pkgs, setPkgs] = useState(false)
  const [busy, setBusy] = useState(false)
  const go = async () => {
    if (pw.length < 8) return message.warning('备份密码至少 8 位')
    setBusy(true)
    try {
      await api.system.backup(pw, pkgs)
      message.success('备份已下载，请把文件和密码分开妥善保存')
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <PageCard title="备份系统数据">
      {data && (
        <Descriptions size="small" column={3} style={{ marginBottom: 14 }}>
          <Descriptions.Item label="用户">{data.users}</Descriptions.Item>
          <Descriptions.Item label="订单">{data.orders}</Descriptions.Item>
          <Descriptions.Item label="节点">{data.nodes}</Descriptions.Item>
          <Descriptions.Item label="数据库">{bytes(data.db_bytes)}</Descriptions.Item>
          <Descriptions.Item label="安装包" span={2}>{data.packages} 个 · {bytes(data.package_bytes)}</Descriptions.Item>
        </Descriptions>
      )}
      <p className="muted">
        包含全部用户、订单、套餐、节点、设置、客服、版本记录，以及 JWT 密钥、后台密码、支付密钥（恢复后用户无需重新登录）。
        文件使用你设置的密码以 AES-256 加密，<b>忘记密码将无法恢复</b>。
      </p>
      <Space wrap>
        <Input.Password placeholder="设置备份密码（≥ 8 位）" style={{ width: 240 }} value={pw} onChange={(e) => setPw(e.target.value)} autoComplete="new-password" />
        <Checkbox checked={pkgs} onChange={(e) => setPkgs(e.target.checked)}>
          包含安装包{data?.package_bytes ? `（+${bytes(data.package_bytes)}）` : ''}
        </Checkbox>
        <Button type="primary" icon={<CloudDownloadOutlined />} loading={busy} onClick={go}>下载备份</Button>
      </Space>
    </PageCard>
  )
}

function RestoreCard() {
  const { message, modal } = App.useApp()
  const [file, setFile] = useState<UploadFile[]>([])
  const [pw, setPw] = useState('')
  const [progress, setProgress] = useState<number>()
  const [done, setDone] = useState<string>()

  const go = () => {
    const f = file[0]?.originFileObj
    if (!f) return message.warning('请选择 .hybk 备份文件')
    if (!pw) return message.warning('请输入备份密码')
    modal.confirm({
      title: '恢复备份',
      content: '当前所有数据将被备份中的数据替换（当前数据库会保留一份 .bak-pre-restore 文件）。恢复期间服务会重启约 5 秒。确定继续？',
      okText: '恢复',
      okButtonProps: { danger: true },
      onOk: async () => {
        const fd = new FormData()
        fd.append('password', pw) // before the file: the server needs it to decrypt while streaming
        fd.append('file', f, file[0].name)
        setProgress(0)
        try {
          const r = await api.system.restore(fd, setProgress)
          setDone(r.message)
          setTimeout(() => window.location.reload(), 6000)
        } catch (e) {
          message.error(errMsg(e))
        } finally {
          setProgress(undefined)
        }
      },
    })
  }
  return (
    <PageCard title="从备份恢复">
      {done ? (
        <Alert type="success" showIcon message={done} description="页面将在几秒后自动刷新。" />
      ) : (
        <>
          <Upload.Dragger
            accept=".hybk"
            maxCount={1}
            fileList={file}
            beforeUpload={() => false}
            onChange={({ fileList }) => setFile(fileList.slice(-1))}
            disabled={progress !== undefined}
          >
            <p className="ant-upload-drag-icon"><InboxOutlined /></p>
            <p className="ant-upload-text">{file[0]?.name ?? '点击或拖拽 .hybk 备份文件到这里'}</p>
            <p className="ant-upload-hint">可恢复本系统或其它环游系统导出的备份</p>
          </Upload.Dragger>
          <Space style={{ marginTop: 14 }} wrap>
            <Input.Password placeholder="备份密码" style={{ width: 240 }} value={pw} onChange={(e) => setPw(e.target.value)} />
            <Button danger icon={<RollbackOutlined />} loading={progress !== undefined} onClick={go}>开始恢复</Button>
          </Space>
          {progress !== undefined && <Progress percent={Math.round(progress * 100)} status="active" style={{ marginTop: 10 }} />}
        </>
      )}
    </PageCard>
  )
}

type Phase = 'form' | 'running' | 'done' | 'failed'

function CloneCard() {
  const { message } = App.useApp()
  const [form] = Form.useForm()
  const [phase, setPhase] = useState<Phase>('form')
  const [log, setLog] = useState('')
  const [error, setError] = useState<string>()
  const [result, setResult] = useState<{ url: string; admin: string; tls: boolean }>()
  const logRef = useRef<HTMLPreElement>(null)
  // Braces matter: newer Chrome returns a Promise from scrollTo, and React
  // would call a returned value as the effect cleanup and crash the page.
  useEffect(() => {
    logRef.current?.scrollTo({ top: logRef.current.scrollHeight })
  }, [log])

  const go = async () => {
    const v = await form.validateFields()
    setPhase('running')
    setLog('')
    setError(undefined)
    try {
      await api.system.clone(
        {
          host: v.host.trim(), port: v.port, user: v.user, password: v.password,
          domain: v.domain.trim(), email: v.email?.trim(), admin_password: v.admin_password,
          mode: v.mode, keep_nodes: !!v.keep_nodes,
        },
        (e) => {
          if (e.type === 'log') setLog((l) => l + e.text)
          else if (e.type === 'error') {
            setError(e.message)
            setPhase('failed')
          } else {
            setResult(e)
            setPhase('done')
            message.success('新系统已部署')
          }
        },
      )
      setPhase((p) => (p === 'running' ? 'failed' : p))
    } catch (e) {
      setError(errMsg(e))
      setPhase('failed')
    }
  }

  return (
    <PageCard title="克隆部署一套新系统">
      {phase === 'form' ? (
        <Form form={form} layout="vertical" requiredMark={false}
          initialValues={{ port: 22, user: 'root', mode: 'config', keep_nodes: false }}>
          <Alert
            type="info"
            showIcon
            style={{ marginBottom: 14 }}
            message="把本系统（程序 + 数据）一键部署到另一台全新服务器，自动申请 HTTPS 证书。"
            description="准备：一台干净的 Debian / Ubuntu（x86_64，80 和 443 端口空闲），并先把新域名的 A 记录解析到这台服务器（不要开 CDN 代理）。"
          />
          <div style={{ display: 'flex', gap: 12 }}>
            <Form.Item name="host" label="服务器 IP" rules={[{ required: true, message: '必填' }]} style={{ flex: 1 }}><Input placeholder="203.0.113.10" /></Form.Item>
            <Form.Item name="port" label="SSH 端口" style={{ width: 100 }}><InputNumber min={1} max={65535} style={{ width: '100%' }} /></Form.Item>
            <Form.Item name="user" label="用户" style={{ width: 100 }}><Input /></Form.Item>
          </div>
          <Form.Item name="password" label="SSH 密码" rules={[{ required: true, message: '必填' }]}><Input.Password autoComplete="new-password" /></Form.Item>
          <Form.Item name="domain" label="新系统域名" rules={[{ required: true, pattern: /^([a-z0-9-]+\.)+[a-z]{2,}$/i, message: '如 vpn2.example.com' }]}
            extra="必须已解析到上面的服务器"><Input placeholder="vpn2.example.com" /></Form.Item>
          <div style={{ display: 'flex', gap: 12 }}>
            <Form.Item name="admin_password" label="新系统后台密码" rules={[{ required: true, min: 8, message: '至少 8 位' }]} style={{ flex: 1 }}>
              <Input.Password autoComplete="new-password" />
            </Form.Item>
            <Form.Item name="email" label="证书邮箱（可选）" style={{ flex: 1 }}><Input placeholder="ops@example.com" /></Form.Item>
          </div>
          <Form.Item name="mode" label="复制内容">
            <Segmented options={[
              { label: '仅配置（套餐、设置、客服、版本）', value: 'config' },
              { label: '完整克隆（含用户和订单）', value: 'full' },
            ]} />
          </Form.Item>
          <Form.Item name="keep_nodes" label="保留节点列表" valuePropName="checked"
            extra="一般不开：节点只能向一个后台汇报。新系统可用「一键安装节点」装自己的节点。">
            <Switch />
          </Form.Item>
          <Button type="primary" icon={<CloudServerOutlined />} onClick={go}>开始克隆</Button>
        </Form>
      ) : (
        <>
          <div className="provision-status">
            {phase === 'running' && <><LoadingOutlined /> 正在部署，通常 1–3 分钟，请勿关闭页面…</>}
            {phase === 'done' && result && (
              <><CheckCircleFilled style={{ color: '#12A150' }} /> 已部署：<a href={result.admin} target="_blank" rel="noreferrer">{result.admin}</a>
                {!result.tls && <span className="muted">（证书申请中，稍后访问）</span>}</>
            )}
            {phase === 'failed' && <><CloseCircleFilled style={{ color: '#D64545' }} /> 失败：{error}</>}
          </div>
          <pre ref={logRef} className="provision-log">{log || '等待服务器响应…'}</pre>
          {phase !== 'running' && <Button style={{ marginTop: 12 }} onClick={() => setPhase('form')}>返回</Button>}
        </>
      )}
    </PageCard>
  )
}
