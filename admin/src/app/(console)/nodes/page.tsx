'use client'
import { ClockCircleOutlined, CloudServerOutlined, DownOutlined, LockOutlined, PlusOutlined, SearchOutlined, SyncOutlined, UnlockOutlined, WarningFilled } from '@ant-design/icons'
import { Alert, App, Button, Collapse, Drawer, Dropdown, Form, Input, InputNumber, Modal, Segmented, Select, Space, Spin, Switch, Table, Tag, Tooltip, Typography } from 'antd'
import { useEffect, useMemo, useState } from 'react'
import { NumberUnit } from '@/components/NumberUnit'
import { NodeHealth } from '@/components/NodeHealth'
import { PageCard } from '@/components/PageCard'
import { useAction, useAsync } from '@/hooks/useAsync'
import { useCatalog } from '@/hooks/useCatalog'
import { api, type VpnNode } from '@/lib/api'
import { bytes, dateTime, errMsg, expiryText, flag } from '@/lib/format'
import { ProvisionModal } from './ProvisionModal'
import { RentalFields } from '@/components/ExpiryField'
import { FoldAlert } from '@/components/FoldAlert'

const blank = (): VpnNode => ({
  id: '', name: '', region: 'US', address: '', token: '', reality_target: '', vision_port: 443, xhttp_path: '',
  disabled: false, frozen: false, frozen_reason: '', note: '', tags: [], rate: 1, plans: [], bandwidth_mbps: 0,
  weight: 1, sort: 0, expires_at: null, vendor: '', vendor_url: '',
  reality: { public_key: '', short_id: '', server_name: 'www.apple.com', fingerprint: 'chrome' },
})
// Rental expiry severity for marks: expired / within 48 h / within 7 days.
type Expiry = 'expired' | 'urgent' | 'soon' | 'ok' | 'none'
const expiryOf = (n: VpnNode): Expiry => {
  if (!n.expires_at) return 'none'
  const ms = Date.parse(n.expires_at) - Date.now()
  return ms <= 0 ? 'expired' : ms <= 48 * 3600000 ? 'urgent' : ms <= 7 * 86400000 ? 'soon' : 'ok'
}
const expiryWarn = (e: Expiry) => e === 'expired' || e === 'urgent' || e === 'soon'
const isExpired = (n: VpnNode) => !!n.expires_at && Date.parse(n.expires_at) <= Date.now()
// Expiry buckets for the statistics row: expired, or due within N days (cumulative).
const buckets = [
  { key: 'expired', label: '已过期', days: 0 },
  { key: 'd1', label: '1 天内到期', days: 1 },
  { key: 'd3', label: '3 天内到期', days: 3 },
  { key: 'd7', label: '7 天内到期', days: 7 },
  { key: 'd15', label: '15 天内到期', days: 15 },
] as const
type Bucket = (typeof buckets)[number]['key']
const inBucket = (n: VpnNode, b: Bucket) => {
  if (!n.expires_at) return false
  const ms = Date.parse(n.expires_at) - Date.now()
  const d = buckets.find((x) => x.key === b)!.days
  return b === 'expired' ? ms <= 0 : ms > 0 && ms <= d * 86400000
}
const expiryColor: Record<Expiry, string> = { expired: 'error', urgent: 'volcano', soon: 'orange', ok: 'default', none: 'default' }

// Status is response-only; the target follows the SNI.
const toBody = ({ status: _s, ...n }: VpnNode) => ({ ...n, reality_target: n.reality.server_name + ':443' }) as VpnNode

export default function NodesPage() {
  const { message, modal } = App.useApp()
  const c = useCatalog(api.nodes, blank, '节点')
  const { busy: checking, run } = useAction()
  const region = Form.useWatch('region', c.form)
  const [install, setInstall] = useState<{ node: VpnNode; data?: { node_json: string; steps: string }; err?: string }>()
  const [provisionOpen, setProvisionOpen] = useState(false)
  const { data: planPage } = useAsync(api.plans.list)
  const plans = planPage?.items ?? []

  // ---- search / filter / selection ----
  const [q, setQ] = useState('')
  const [statusF, setStatusF] = useState<'all' | 'ok' | 'down' | 'expired' | 'frozen' | 'off'>('all')
  const [bucketF, setBucketF] = useState<Bucket>()
  const [tagF, setTagF] = useState<string[]>([])
  const [selected, setSelected] = useState<string[]>([])
  const allTags = useMemo(() => [...new Set(c.items.flatMap((n) => n.tags ?? []))].sort(), [c.items])
  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase()
    return c.items.filter((n) => {
      if (needle && ![n.id, n.name, n.address, n.region, n.note, ...(n.tags ?? [])].some((v) => v?.toLowerCase().includes(needle))) return false
      if (tagF.length && !tagF.every((t) => n.tags?.includes(t))) return false
      if (bucketF && !inBucket(n, bucketF)) return false
      switch (statusF) {
        case 'ok': return live(n) && !!n.status?.online
        case 'down': return live(n) && !n.status?.online
        case 'expired': return isExpired(n)
        case 'frozen': return n.frozen
        case 'off': return n.disabled
      }
      return true
    })
  }, [c.items, q, statusF, tagF, bucketF])
  const count = (f: (n: VpnNode) => boolean) => c.items.filter(f).length
  // Expired nodes are no longer served to the app, so they are neither 正常 nor 离线.
  function live(n: VpnNode) {
    return !n.frozen && !n.disabled && !isExpired(n)
  }
  const expiring = useMemo(
    () => c.items.filter((n) => expiryWarn(expiryOf(n))).sort((a, b) => Date.parse(a.expires_at!) - Date.parse(b.expires_at!)),
    [c.items],
  )
  // ---- freeze ----
  const [freezing, setFreezing] = useState<VpnNode[]>()
  const [freezeReason, setFreezeReason] = useState('')
  const doFreeze = async () => {
    for (const n of freezing ?? []) await c.patch(n, { frozen: true, frozen_reason: freezeReason.trim() }, toBody)
    message.success(`已冻结 ${freezing?.length} 个节点，用户连接会在 30 秒内断开`)
    setFreezing(undefined)
    setFreezeReason('')
    setSelected([])
  }
  const unfreeze = async (list: VpnNode[]) => {
    for (const n of list) await c.patch(n, { frozen: false, frozen_reason: '' }, toBody)
    message.success(`已解冻 ${list.length} 个节点`)
    setSelected([])
  }
  const bulk = async (change: Partial<VpnNode>, label: string) => {
    const list = c.items.filter((n) => selected.includes(n.id))
    for (const n of list) await c.patch(n, change, toBody)
    message.success(`${label} ${list.length} 个节点`)
    setSelected([])
  }

  // Manual form: fill region (and name, if empty) from the address.
  const geoFill = async () => {
    const addr = (c.form.getFieldValue('address') as string | undefined)?.trim()
    if (!addr || c.mode !== 'create') return
    try {
      const g = await api.nodes.geoip(addr)
      c.form.setFieldsValue({ region: g.country_code, ...(c.form.getFieldValue('name') ? {} : { name: g.suggested_name }) })
      message.info(`已识别地区：${flag(g.country_code)} ${g.country}${g.city ? ' · ' + g.city : ''}`)
    } catch {
      // keep whatever the operator typed
    }
  }

  // Refresh health every 30 s unless a form is open.
  useEffect(() => {
    const t = setInterval(() => !c.open && c.load(true), 30000)
    return () => clearInterval(t)
  }, [c])

  const showInstall = async (node: VpnNode) => {
    setInstall({ node })
    try {
      setInstall({ node, data: await api.nodes.install(node.id) })
    } catch (e) {
      setInstall({ node, err: errMsg(e) })
    }
  }

  const save = async () => {
    const saved = await c.save(toBody)
    if (saved && c.mode === 'create') showInstall(saved)
  }

  // Push the API server's current vpn-node binary to this node over SSH.
  const [upgrading, setUpgrading] = useState<VpnNode>()
  const [upForm] = Form.useForm()
  const [upBusy, setUpBusy] = useState(false)
  const [upLog, setUpLog] = useState('')
  const openUpgrade = (n: VpnNode) => {
    upForm.setFieldsValue({ user: 'root', port: 22, password: '' })
    setUpLog('')
    setUpgrading(n)
  }
  const doUpgrade = async () => {
    const v = await upForm.validateFields()
    setUpBusy(true)
    setUpLog('')
    try {
      const r = await api.nodes.upgrade(upgrading!.id, v)
      setUpLog(r.log)
      message.success('节点程序已升级并重启')
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setUpBusy(false)
    }
  }

  const rotateToken = (n: VpnNode) =>
    modal.confirm({
      title: '重置 Token',
      content: '旧 Token 立即失效，需要把新的 node.json 更新到节点服务器并重启 vpn-node。',
      okButtonProps: { danger: true },
      onOk: async () => {
        if (await run(() => api.nodes.rotateToken(n.id), 'Token 已重置')) showInstall(n)
      },
    })

  return (
    <div className="page">
      <PageCard>
        <div className="toolbar">
          <span className="muted">填服务器 IP + SSH 密码即可自动安装上线；已有节点可手动添加</span>
          <div className="spacer" />
          <Button
            icon={<SyncOutlined spin={checking} />}
            loading={checking}
            onClick={() => run(async () => c.setItems((await api.monitor.checkNow()).items), '检测完成')}
          >
            立即检测
          </Button>
          <Button icon={<PlusOutlined />} onClick={() => c.openCreate({ sort: (c.items.at(-1)?.sort ?? 0) + 10 })}>手动添加</Button>
          <Button type="primary" icon={<CloudServerOutlined />} onClick={() => setProvisionOpen(true)}>一键安装节点</Button>
        </div>
        <div className="toolbar">
          <Input
            allowClear
            prefix={<SearchOutlined />}
            placeholder="搜索名称 / ID / IP / 地区 / 标签 / 备注"
            style={{ width: 300 }}
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
          <Segmented
            value={statusF}
            onChange={(v) => setStatusF(v as typeof statusF)}
            options={[
              { label: `全部 ${c.items.length}`, value: 'all' },
              { label: `正常 ${count((n) => live(n) && !!n.status?.online)}`, value: 'ok' },
              { label: `离线 ${count((n) => live(n) && !n.status?.online)}`, value: 'down' },
              {
                label: count(isExpired) ? <span style={{ color: '#CF1322' }}>已过期 {count(isExpired)}</span> : '已过期 0',
                value: 'expired',
              },
              { label: `冻结 ${count((n) => n.frozen)}`, value: 'frozen' },
              { label: `已下架 ${count((n) => n.disabled)}`, value: 'off' },
            ]}
          />
          {allTags.length > 0 && (
            <Select mode="multiple" allowClear placeholder="按标签筛选" style={{ minWidth: 160 }} value={tagF} onChange={setTagF}
              options={allTags.map((t) => ({ value: t, label: t }))} />
          )}
          <div className="spacer" />
          {selected.length > 0 && (
            <Space>
              <span className="muted">已选 {selected.length}</span>
              <Button icon={<LockOutlined />} onClick={() => setFreezing(c.items.filter((n) => selected.includes(n.id)))}>冻结</Button>
              <Button icon={<UnlockOutlined />} onClick={() => unfreeze(c.items.filter((n) => selected.includes(n.id) && n.frozen))}>解冻</Button>
              <Button onClick={() => bulk({ disabled: false }, '已上架')}>上架</Button>
              <Button onClick={() => bulk({ disabled: true }, '已下架')}>下架</Button>
            </Space>
          )}
        </div>
        <div className="toolbar" style={{ gap: 8 }}>
          <Tooltip title="按服务器到期时间统计，点击筛选。已过期的节点不再下发给 App（节点列表、智能 / 集群 / AI 线路），也不再做在线检测。">
            <span className="muted" style={{ cursor: 'help' }}><ClockCircleOutlined /> 到期统计：</span>
          </Tooltip>
          {buckets.map((b) => {
            const n = count((x) => inBucket(x, b.key))
            const on = bucketF === b.key
            const color = b.key === 'expired' || b.key === 'd1' ? 'red' : b.key === 'd3' ? 'volcano' : b.key === 'd7' ? 'orange' : 'gold'
            return (
              <Tag.CheckableTag
                key={b.key}
                checked={on}
                onChange={(v) => setBucketF(v ? b.key : undefined)}
                className={'expiry-chip expiry-' + color}
                style={{ opacity: n || on ? 1 : 0.55 }}
              >
                {b.label} <b>{n}</b>
              </Tag.CheckableTag>
            )
          })}
          {bucketF && <Button type="link" size="small" onClick={() => setBucketF(undefined)}>清除筛选</Button>}
        </div>
        {expiring.length > 0 && (
          <FoldAlert
            id="nodes_expiring"
            type={expiring.some((n) => expiryOf(n) !== 'soon') ? 'error' : 'warning'}
            showIcon
            style={{ marginBottom: 12, paddingBlock: 8 }}
            message={
              <span>
                {expiring.length} 个节点服务器即将到期或已过期：
                <span className="muted" style={{ marginLeft: 4 }}>
                  {expiring.map((n) => `${n.name}（${expiryText(n.expires_at)}）`).join('、')}
                </span>
              </span>
            }
            description={expiring.map((n) => (
              <div key={n.id}>
                <Tag bordered={false} color={expiryColor[expiryOf(n)]}>{expiryText(n.expires_at)}</Tag>
                <b>{n.name}</b> <span className="muted">{n.address} · {dateTime(n.expires_at)}{n.vendor ? ` · ${n.vendor}` : ''}</span>
                {n.vendor_url && <a href={n.vendor_url} target="_blank" rel="noreferrer" style={{ marginLeft: 8 }}>去续费</a>}
              </div>
            ))}
          />
        )}
        <Table<VpnNode>
          rowKey="id"
          loading={c.loading}
          dataSource={shown}
          pagination={false}
          scroll={{ x: 1470 }}
          rowSelection={{ selectedRowKeys: selected, onChange: (k) => setSelected(k as string[]) }}
          rowClassName={(n) => {
            const e = expiryOf(n)
            return n.frozen ? 'row-frozen' : e === 'expired' || e === 'urgent' ? 'row-expired' : e === 'soon' ? 'row-expiring' : ''
          }}
          columns={[
            {
              title: '节点',
              render: (_, n) => (
                <>
                  <div style={{ fontWeight: 500 }}>
                    {flag(n.region)} {n.name}
                    {expiryWarn(expiryOf(n)) && (
                      <Tooltip title={`服务器 ${dateTime(n.expires_at)} 到期${n.vendor ? `（${n.vendor}）` : ''}`}>
                        <Tag color={expiryColor[expiryOf(n)]} icon={<WarningFilled />} style={{ marginLeft: 6 }}>
                          {expiryOf(n) === 'expired' ? '已过期' : '即将到期'}
                        </Tag>
                      </Tooltip>
                    )}
                    {n.frozen && <Tag color="blue" icon={<LockOutlined />} style={{ marginLeft: 6 }}>冻结</Tag>}
                    {n.disabled && <Tag style={{ marginLeft: 6 }}>已下架</Tag>}
                    {n.rate !== 1 && <Tag color="orange" bordered={false} style={{ marginLeft: 6 }}>×{n.rate}</Tag>}
                  </div>
                  <div className="muted">{n.id} · {n.address}:{n.vision_port}</div>
                  {(n.tags?.length ?? 0) > 0 && <div style={{ marginTop: 2 }}>{n.tags.map((t) => <Tag key={t} bordered={false} style={{ fontSize: 11 }}>{t}</Tag>)}</div>}
                  {n.frozen && n.frozen_reason && <div className="muted" style={{ color: '#2f5fa8' }}>冻结原因：{n.frozen_reason}</div>}
                  {n.note && <Tooltip title={n.note}><div className="muted" style={{ maxWidth: 260, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>备注：{n.note}</div></Tooltip>}
                </>
              ),
            },
            {
              title: '状态（近 1 小时）',
              width: 280,
              render: (_, n) =>
                isExpired(n) ? (
                  <Tooltip title="服务器租期已到，App 不再获取这个节点，也不再检测。续费后修改到期时间即可恢复。">
                    <Tag color="error" bordered={false}>已过期 · 已停止下发</Tag>
                  </Tooltip>
                ) : (
                  <NodeHealth status={n.status} disabled={n.disabled} history />
                ),
            },
            { title: '可用率', width: 80, render: (_, n) => (n.status ? Math.round(n.status.uptime * 100) + '%' : '—') },
            {
              title: '到期 / 厂商',
              width: 170,
              sorter: (a, b) => (a.expires_at ? Date.parse(a.expires_at) : Infinity) - (b.expires_at ? Date.parse(b.expires_at) : Infinity),
              render: (_, n) => {
                const lv = expiryOf(n)
                return (
                  <>
                    {lv === 'none' ? (
                      <span className="muted">未设置到期</span>
                    ) : (
                      <Tooltip title={dateTime(n.expires_at)}>
                        <Tag bordered={false} color={expiryColor[lv]} icon={expiryWarn(lv) ? <WarningFilled /> : <ClockCircleOutlined />}>
                          {expiryText(n.expires_at)}
                        </Tag>
                      </Tooltip>
                    )}
                    <div className="muted">
                      {n.vendor_url ? <a href={n.vendor_url} target="_blank" rel="noreferrer">{n.vendor || '续费入口'}</a> : n.vendor || '—'}
                    </div>
                  </>
                )
              },
            },
            {
              title: '在线 / 负载',
              width: 110,
              render: (_, n) => (
                <>
                  <div>{n.status?.active_users ?? 0} 人在用</div>
                  <div className="muted">{n.bandwidth_mbps ? `带宽 ${n.bandwidth_mbps} Mbps` : '未设带宽'}</div>
                </>
              ),
            },
            { title: '可用套餐', width: 120, render: (_, n) => (n.plans?.length ? n.plans.map((p) => plans.find((x) => x.id === p)?.name ?? p).join('、') : <span className="muted">全部</span>) },
            { title: '伪装 SNI', render: (_, n) => <span className="mono">{n.reality.server_name}</span> },
            { title: '累计流量', width: 100, render: (_, n) => bytes(n.status?.traffic_bytes) },
            { title: '上架', width: 70, render: (_, n) => <Switch checked={!n.disabled} onChange={(v) => c.patch(n, { disabled: !v }, toBody)} /> },
            {
              title: '操作',
              width: 200,
              fixed: 'right',
              render: (_, n) => (
                <Space size={0}>
                  <Button type="link" size="small" onClick={() => c.openEdit(n)}>编辑</Button>
                  <Button type="link" size="small" onClick={() => showInstall(n)}>部署配置</Button>
                  <Dropdown
                    trigger={['click']}
                    menu={{
                      items: [
                        n.frozen ? { key: 'unfreeze', label: '解冻节点' } : { key: 'freeze', label: '冻结节点' },
                        { key: 'upgrade', label: '升级节点程序' },
                        { key: 'token', label: '重置 Token' },
                        { type: 'divider' },
                        { key: 'delete', label: '删除节点', danger: true },
                      ],
                      onClick: ({ key }) =>
                        key === 'delete' ? c.remove(n)
                          : key === 'upgrade' ? openUpgrade(n)
                            : key === 'freeze' ? setFreezing([n])
                              : key === 'unfreeze' ? unfreeze([n])
                                : rotateToken(n),
                    }}
                  >
                    <Button type="link" size="small">更多 <DownOutlined /></Button>
                  </Dropdown>
                </Space>
              ),
            },
          ]}
        />
      </PageCard>

      <Drawer
        open={c.open}
        onClose={c.close}
        size={540}
        title={c.mode === 'create' ? '新增节点' : '编辑节点'}
        extra={<Space><Button onClick={c.close}>取消</Button><Button type="primary" loading={c.saving} onClick={save}>保存</Button></Space>}
      >
        <Form form={c.form} layout="vertical">
          <Space size={16} style={{ display: 'flex' }}>
            <Form.Item name="id" label="ID" rules={[{ required: true, pattern: /^[a-z0-9][a-z0-9_-]{0,31}$/, message: '小写字母 / 数字 / - / _' }]}>
              <Input disabled={c.mode === 'edit'} placeholder="如 us1、jp2" />
            </Form.Item>
            <Form.Item name="region" label="地区代码" rules={[{ required: true, len: 2, message: '两位国家代码' }]} normalize={(v: string) => v?.toUpperCase()}>
              <Input maxLength={2} placeholder="US" style={{ width: 110 }} suffix={<span style={{ fontSize: 18 }}>{flag(region ?? '')}</span>} />
            </Form.Item>
          </Space>
          <Form.Item name="name" label="名称" rules={[{ required: true }]}><Input placeholder="如 美国 01" /></Form.Item>
          <div style={{ display: 'flex', gap: 16, alignItems: 'flex-start' }}>
            <Form.Item name="address" label="地址" rules={[{ required: true }]} style={{ flex: 1 }}>
              <Input placeholder="公网 IP 或域名（填写后自动识别地区）" onBlur={geoFill} />
            </Form.Item>
            <Form.Item name="vision_port" label="端口（建议 443）"><InputNumber min={1} max={65535} style={{ width: 130 }} /></Form.Item>
          </div>
          <Form.Item
            name={['reality', 'server_name']}
            label="伪装域名 SNI"
            rules={[{ required: true }]}
            extra="必须是境外、支持 TLS1.3 + H2、走经典 X25519 的真实网站。使用抗量子密钥交换的站点（多数 Cloudflare / Google 站）会导致连接失败。"
          >
            <Input placeholder="www.apple.com" />
          </Form.Item>
          <Space size={16}>
            <Form.Item name={['reality', 'fingerprint']} label="TLS 指纹">
              <Select style={{ width: 140 }} options={['chrome', 'firefox', 'safari', 'ios', 'edge'].map((v) => ({ value: v }))} />
            </Form.Item>
            <Form.Item name="sort" label="排序"><InputNumber /></Form.Item>
            <Form.Item name="weight" label="权重"><InputNumber min={1} /></Form.Item>
          </Space>
          <Form.Item name="tags" label="标签" extra="用于搜索和分组，例如：高速、流媒体、原生 IP、备用">
            <Select mode="tags" tokenSeparators={[',', '，', ' ']} placeholder="输入后回车" options={allTags.map((t) => ({ value: t }))} />
          </Form.Item>
          <Space size={16} style={{ display: 'flex' }} wrap>
            <Form.Item name="rate" label="流量倍率" tooltip="用户在此节点用 1 GB 记为 倍率 × 1 GB；专线可设 1.5，低价节点可设 0.5">
              <InputNumber min={0} max={10} step={0.1} style={{ width: 110 }} />
            </Form.Item>
            <Form.Item name="bandwidth_mbps" label="节点带宽" tooltip="填写服务器实际带宽后启用智能分配：负载超过 85% 时自动把带宽公平分给在线用户，防止个别大流量用户占满；0 = 不启用">
              <NumberUnit min={0} max={100000} unit="Mbps" width={110} />
            </Form.Item>
          </Space>
          <Form.Item name="plans" label="可用套餐" extra="不选 = 所有套餐都能用；选了则只有这些套餐的用户能看到和连接此节点">
            <Select mode="multiple" allowClear placeholder="全部套餐" options={plans.map((p) => ({ value: p.id, label: p.name }))} />
          </Form.Item>
          <RentalFields required={c.mode === 'create'} />
          <Form.Item name="note" label="备注" extra="仅后台可见，例如成本、线路说明">
            <Input.TextArea rows={2} maxLength={300} />
          </Form.Item>
          {c.mode === 'create' ? (
            <Collapse
              ghost
              items={[
                {
                  key: 'import',
                  label: '导入已有节点（可选）',
                  children: (
                    <>
                      <Alert type="info" showIcon style={{ marginBottom: 12 }} message="留空则自动生成新密钥。导入时需与节点服务器 node.json 中的私钥配对。" />
                      <Form.Item name={['reality', 'public_key']} label="REALITY 公钥"><Input /></Form.Item>
                      <Form.Item name={['reality', 'short_id']} label="Short ID"><Input /></Form.Item>
                      <Form.Item name="xhttp_path" label="XHTTP 路径"><Input placeholder="/xxxx" /></Form.Item>
                    </>
                  ),
                },
              ]}
            />
          ) : (
            <>
              <Form.Item label="REALITY 公钥 / Short ID">
                <Typography.Text className="mono" copyable>{c.form.getFieldValue(['reality', 'public_key'])}</Typography.Text>
                <div className="mono">{c.form.getFieldValue(['reality', 'short_id'])}</div>
              </Form.Item>
              <Form.Item name="xhttp_path" label="XHTTP 路径"><Input /></Form.Item>
            </>
          )}
        </Form>
      </Drawer>

      <Modal
        open={!!upgrading}
        title={`升级节点程序 · ${upgrading?.name ?? ''}`}
        onCancel={() => !upBusy && setUpgrading(undefined)}
        onOk={doUpgrade}
        okText="开始升级"
        confirmLoading={upBusy}
        destroyOnHidden
      >
        <p className="muted">把后台当前版本的 vpn-node 上传到 {upgrading?.address} 并重启，配置保持不变（连接会中断几秒）。</p>
        <Form form={upForm} layout="vertical">
          <Space size={12}>
            <Form.Item name="user" label="SSH 用户"><Input style={{ width: 120 }} /></Form.Item>
            <Form.Item name="port" label="SSH 端口"><InputNumber min={1} max={65535} style={{ width: 110 }} /></Form.Item>
          </Space>
          <Form.Item name="password" label="SSH 密码" rules={[{ required: true, message: '请填写 SSH 密码' }]}>
            <Input.Password autoComplete="new-password" />
          </Form.Item>
        </Form>
        {upLog && <pre className="provision-log" style={{ height: 160 }}>{upLog}</pre>}
      </Modal>

      <Modal
        open={!!freezing}
        title={`冻结节点${freezing && freezing.length > 1 ? `（${freezing.length} 个）` : ` · ${freezing?.[0]?.name ?? ''}`}`}
        okText="冻结"
        okButtonProps={{ danger: true }}
        onOk={doFreeze}
        onCancel={() => setFreezing(undefined)}
        destroyOnHidden
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message="冻结后节点立即对用户隐藏，节点程序在下次同步（30 秒内）清空用户、断开所有连接；配置和密钥保留，解冻即恢复。"
        />
        <Input.TextArea rows={2} placeholder="冻结原因（可选），如：IP 被墙 / 欠费 / 维护" value={freezeReason} onChange={(e) => setFreezeReason(e.target.value)} />
      </Modal>

      <ProvisionModal open={provisionOpen} onClose={() => setProvisionOpen(false)} onDone={() => c.load(true)} />

      <Modal open={!!install} onCancel={() => setInstall(undefined)} footer={null} width={700} title={`部署配置 · ${install?.node.name ?? ''}`}>
        {install?.err ? (
          <Alert type="warning" showIcon message={install.err} />
        ) : !install?.data ? (
          <Spin style={{ display: 'block', margin: 40 }} />
        ) : (
          <>
            <p className="muted">保存为节点服务器的 <code>/etc/vpn/node.json</code>（含私钥，注意保密）：</p>
            <div className="code-block">
              <pre>{install.data.node_json}</pre>
              <Button
                size="small"
                style={{ position: 'absolute', top: 8, right: 8 }}
                onClick={() => navigator.clipboard.writeText(install.data!.node_json).then(() => message.success('已复制'))}
              >
                复制
              </Button>
            </div>
            <p className="muted">安装步骤：</p>
            <div className="code-block"><pre>{install.data.steps}</pre></div>
          </>
        )}
      </Modal>
    </div>
  )
}
