'use client'
import { CheckCircleFilled, CloudDownloadOutlined, CloudServerOutlined, CloseCircleFilled, InboxOutlined, LoadingOutlined, ReloadOutlined, RollbackOutlined } from '@ant-design/icons'
import { Alert, App, Button, Checkbox, Col, Descriptions, Form, Input, InputNumber, Progress, Row, Segmented, Space, Switch, Tag, Upload } from 'antd'
import type { UploadFile } from 'antd'
import { useEffect, useRef, useState } from 'react'
import { FoldAlert } from '@/components/FoldAlert'
import { PageCard } from '@/components/PageCard'
import { useAsync } from '@/hooks/useAsync'
import { api, type ServerInfo } from '@/lib/api'
import { bytes, errMsg } from '@/lib/format'

export default function SystemPage() {
  return (
    <div className="page">
      <Row gutter={16}>
        <Col xs={24}>
          <ServerCard />
          <div style={{ height: 16 }} />
        </Col>
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

const tlsModes: Record<ServerInfo['tls_mode'], string> = {
  tls: 'vpn-api 直接提供 HTTPS（独占 443）',
  tls_shared: 'vpn-api 提供 HTTPS，与本机节点按域名共用 443',
  behind_proxy: '由 nginx 等反向代理提供 HTTPS',
  plain: 'HTTP（未启用 HTTPS）',
}

function uptime(sec: number) {
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  return d ? `${d} 天 ${h} 小时` : h ? `${h} 小时 ${m} 分` : `${m} 分钟`
}

function ServerCard() {
  const { data: s, loading, reload } = useAsync(api.system.serverInfo)
  const memUsed = s?.memory ? 1 - s.memory.available / s.memory.total : undefined
  const diskUsed = s?.disk ? 1 - s.disk.free / s.disk.total : undefined
  const pct = (v?: number) => (v === undefined ? '—' : `${Math.round(v * 100)}%`)
  return (
    <PageCard title="当前服务器" extra={<Button size="small" icon={<ReloadOutlined />} loading={loading} onClick={() => reload()}>刷新</Button>}>
      {!s ? (
        <div className="muted">{loading ? '读取中…' : '读取失败'}</div>
      ) : (
        <>
          {s.domain && !s.points_here && (
            <Alert type="warning" showIcon style={{ marginBottom: 12 }}
              message={`域名 ${s.domain} 当前解析到 ${(s.resolved ?? []).join('、') || '（未解析）'}，不是这台服务器（${s.public_ip || '本机'}）`}
              description="用户访问的是另一台服务器上的系统。如果刚做完服务迁移，这是旧服务器，新数据不会再写到这里。" />
          )}
          <Descriptions size="small" column={{ xs: 1, md: 2, xl: 3 }} bordered>
            <Descriptions.Item label="公网 IP"><b>{s.public_ip || '—'}</b></Descriptions.Item>
            <Descriptions.Item label="域名解析">
              {s.domain ? <>{s.domain} → {(s.resolved ?? []).join('、') || '未解析'} {s.points_here ? <Tag color="green" bordered={false}>指向本机</Tag> : <Tag color="orange" bordered={false}>不是本机</Tag>}</> : '—'}
            </Descriptions.Item>
            <Descriptions.Item label="HTTPS 证书">
              {s.cert?.error ? <span className="muted">{s.cert.error}</span> : s.cert?.not_after
                ? <>{s.cert.issuer} · 剩 <b style={{ color: (s.cert.days_left ?? 0) < 15 ? '#D64545' : undefined }}>{s.cert.days_left}</b> 天到期</>
                : '—'}
            </Descriptions.Item>
            <Descriptions.Item label="接入方式">{tlsModes[s.tls_mode]}</Descriptions.Item>
            <Descriptions.Item label="本机节点">
              {s.nodes_here?.length ? s.nodes_here.map((n) => <Tag key={n.id} bordered={false} color={n.disabled ? 'default' : 'blue'}>{n.name}{n.disabled ? '（停用）' : ''}</Tag>) : <span className="muted">无</span>}
              {s.passthrough && <span className="muted">（节点在 {s.passthrough}）</span>}
            </Descriptions.Item>
            <Descriptions.Item label="主机名">{s.hostname}</Descriptions.Item>
            <Descriptions.Item label="系统">{s.os} · {s.arch}{s.kernel ? ` · 内核 ${s.kernel}` : ''}</Descriptions.Item>
            <Descriptions.Item label="CPU / 负载">{s.cpus} 核 · {(s.load ?? []).map((v) => v.toFixed(2)).join(' / ') || '—'}</Descriptions.Item>
            <Descriptions.Item label="内存">{s.memory ? `${bytes(s.memory.total - s.memory.available)} / ${bytes(s.memory.total)}（${pct(memUsed)}）` : '—'}</Descriptions.Item>
            <Descriptions.Item label="磁盘">{s.disk ? `剩余 ${bytes(s.disk.free)} / ${bytes(s.disk.total)}（已用 ${pct(diskUsed)}）` : '—'}</Descriptions.Item>
            <Descriptions.Item label="运行时间">服务器 {uptime(s.uptime_seconds)} · 后台服务 {uptime(s.process_uptime)}</Descriptions.Item>
            <Descriptions.Item label="数据库 / 程序">{s.db_driver} · {s.binary_sha256 || '—'}</Descriptions.Item>
          </Descriptions>
        </>
      )}
    </PageCard>
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
  const [result, setResult] = useState<{ url: string; admin: string; tls: boolean; migrate?: boolean; ip?: string }>()
  const { data: info } = useAsync(api.system.backupInfo)
  const currentDomain = (info?.public_url ?? '').replace(/^https?:\/\//, '').replace(/\/.*$/, '').toLowerCase()
  const domain = (Form.useWatch('domain', form) ?? '').trim().toLowerCase()
  // Same domain as this system: move the service to a new server, then switch DNS.
  const migrate = !!currentDomain && domain === currentDomain
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
          domain: v.domain.trim(), email: v.email?.trim(), admin_password: v.admin_password ?? '',
          mode: migrate ? 'full' : v.mode, keep_nodes: migrate || !!v.keep_nodes,
        },
        (e) => {
          if (e.type === 'log') setLog((l) => l + e.text)
          else if (e.type === 'error') {
            setError(e.message)
            setPhase('failed')
          } else {
            setResult(e)
            setPhase('done')
            message.success(e.migrate ? '新服务器已部署，请切换域名解析' : '新系统已部署')
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
          <FoldAlert
            id="clone_help"
            type="info"
            showIcon
            style={{ marginBottom: 14, paddingBlock: 8 }}
            message="把本系统（程序 + 数据）一键部署到另一台服务器，自动申请 HTTPS 证书"
            description={<>准备：一台 Debian / Ubuntu（x86_64，80 和 443 端口空闲；已装本系统节点的服务器也可以，节点会自动与后台共用 443，继续可用）。<br />
              · 新域名：先把新域名的 A 记录解析到新服务器（不要开 CDN 代理）。<br />
              · 服务迁移（域名不变）：域名填当前域名 {currentDomain && <b>{currentDomain}</b>}，克隆完成后再把解析改到新服务器。</>}
          />
          <div style={{ display: 'flex', gap: 12 }}>
            <Form.Item name="host" label="服务器 IP" rules={[{ required: true, message: '必填' }]} style={{ flex: 1 }}><Input placeholder="203.0.113.10" /></Form.Item>
            <Form.Item name="port" label="SSH 端口" style={{ width: 100 }}><InputNumber min={1} max={65535} style={{ width: '100%' }} /></Form.Item>
            <Form.Item name="user" label="用户" style={{ width: 100 }}><Input /></Form.Item>
          </div>
          <Form.Item name="password" label="SSH 密码" rules={[{ required: true, message: '必填' }]}><Input.Password autoComplete="new-password" /></Form.Item>
          <Form.Item label="新系统域名" required
            extra={migrate ? '与当前系统相同：服务迁移模式' : '新域名必须已解析到上面的服务器；填当前域名则为服务迁移'}>
            <Space.Compact style={{ width: '100%' }}>
              <Form.Item name="domain" noStyle rules={[{ required: true, pattern: /^([a-z0-9-]+\.)+[a-z]{2,}$/i, message: '如 vpn2.example.com' }]}>
                <Input placeholder="vpn2.example.com" />
              </Form.Item>
              {currentDomain && (
                <Button onClick={() => form.setFieldValue('domain', currentDomain)}>使用当前域名（迁移）</Button>
              )}
            </Space.Compact>
          </Form.Item>
          {migrate && (
            <Alert
              type="warning"
              showIcon
              style={{ marginBottom: 14 }}
              message="服务迁移：域名不变，把整套系统搬到新服务器"
              description={<>
                1. 完整复制用户、订单、节点、设置，并沿用登录密钥：App 用户不用重新登录，节点和支付回调地址都不用改。<br />
                2. 克隆完成后，把 <b>{currentDomain}</b> 的 A 记录改为新服务器 IP；解析生效后首次访问会自动申请 HTTPS 证书。<br />
                3. 克隆之后、切换解析之前新产生的数据（新用户、订单）不会自动同步。可以在切换解析前再克隆一次，覆盖为最新数据。<br />
                4. 当前服务器上的节点（如果有）迁移后不再可用，请在新后台的「节点」页停用或删除。
              </>}
            />
          )}
          <div style={{ display: 'flex', gap: 12 }}>
            <Form.Item name="admin_password" label="新系统后台密码" style={{ flex: 1 }}
              rules={[{ required: !migrate, message: '必填' }, { min: 8, message: '至少 8 位' }]}>
              <Input.Password autoComplete="new-password" placeholder={migrate ? '留空沿用当前密码' : undefined} />
            </Form.Item>
            <Form.Item name="email" label="证书邮箱（可选）" style={{ flex: 1 }}><Input placeholder="ops@example.com" /></Form.Item>
          </div>
          <Form.Item name="mode" label="复制内容" hidden={migrate}>
            <Segmented options={[
              { label: '仅配置（套餐、设置、客服、版本）', value: 'config' },
              { label: '完整克隆（含用户和订单）', value: 'full' },
            ]} />
          </Form.Item>
          <Form.Item name="keep_nodes" label="保留节点列表" valuePropName="checked" hidden={migrate}
            extra="一般不开：节点只能向一个后台汇报。新系统可用「一键安装节点」装自己的节点。">
            <Switch />
          </Form.Item>
          <Button type="primary" icon={<CloudServerOutlined />} onClick={go}>开始克隆</Button>
        </Form>
      ) : (
        <>
          <div className="provision-status">
            {phase === 'running' && <><LoadingOutlined /> 正在部署，通常 1–3 分钟，请勿关闭页面…</>}
            {phase === 'done' && result && !result.migrate && (
              <><CheckCircleFilled style={{ color: '#12A150' }} /> 已部署：<a href={result.admin} target="_blank" rel="noreferrer">{result.admin}</a>
                {!result.tls && <span className="muted">（证书申请中，稍后访问）</span>}</>
            )}
            {phase === 'done' && result?.migrate && (
              <><CheckCircleFilled style={{ color: '#12A150' }} /> 新服务器 {result.ip} 已部署。
                {result.tls ? <>域名已指向新服务器，HTTPS 正常。</> : <>现在把 <b>{currentDomain}</b> 的 A 记录改为 <b>{result.ip}</b>，生效后访问 <a href={result.admin} target="_blank" rel="noreferrer">{result.admin}</a> 即是新服务器。</>}</>
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
