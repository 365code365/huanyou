'use client'
import { ReloadOutlined, SafetyOutlined, StopOutlined } from '@ant-design/icons'
import { Alert, App, Button, Form, Input, InputNumber, Segmented, Select, Space, Switch, Table, Tabs, Tag } from 'antd'
import { useCallback, useEffect, useState } from 'react'
import { PageCard } from '@/components/PageCard'
import { ClassTag, IPCell, IPTable, useBlockDialog } from '@/components/risk'
import { useAction, useAsync } from '@/hooks/useAsync'
import { api, type IPRecord, type RiskConfig, type RiskRule, type SuspectIP, type SuspectUser } from '@/lib/api'
import { dateTime, errMsg } from '@/lib/format'

function Suspicious() {
  const [hours, setHours] = useState(24)
  const { data, loading, reload } = useAsync(() => api.risk.suspicious(hours), [hours])
  const block = useBlockDialog(() => reload(true))
  const cfg = data?.config
  return (
    <>
      <div className="toolbar">
        <Segmented value={hours} onChange={(v) => setHours(v as number)} options={[{ label: '24 小时', value: 24 }, { label: '3 天', value: 72 }, { label: '7 天', value: 168 }]} />
        <span className="muted">
          阈值：单账号 IP 数 &gt; {cfg?.max_ips_per_user ?? '-'}，单 IP 订阅数 &gt; {cfg?.max_users_per_ip ?? '-'}（「设置」里可调）
        </span>
        <div className="spacer" />
        <Button icon={<ReloadOutlined />} onClick={() => reload()}>刷新</Button>
      </div>
      <h4 style={{ margin: '4px 0 8px' }}>多 IP 账号 <span className="muted">（可能共享账号 / 倒卖）</span></h4>
      <Table<SuspectUser>
        rowKey="user_id"
        size="small"
        loading={loading}
        dataSource={data?.multi_ip_users ?? []}
        pagination={{ pageSize: 10, hideOnSinglePage: true }}
        locale={{ emptyText: '没有超过阈值的账号' }}
        columns={[
          { title: '用户', render: (_, u) => <span className="mono">{u.recharge_code}</span> },
          { title: 'IP 数', dataIndex: 'ips', width: 80, sorter: (a, b) => a.ips - b.ips },
          { title: '节点数', dataIndex: 'nodes', width: 80 },
          { title: '套餐', width: 100, render: (_, u) => u.plan_id || <span className="muted">无</span> },
          { title: '最近', width: 150, render: (_, u) => dateTime(u.last_seen) },
          {
            title: '状态',
            width: 90,
            render: (_, u) => (u.blocked ? <Tag color="error">已封禁</Tag> : u.disabled ? <Tag>已停用</Tag> : <Tag color="warning">待核查</Tag>),
          },
          {
            title: '操作',
            width: 150,
            render: (_, u) => (
              <Space size={0}>
                <Button type="link" size="small" href={`/admin/risk/?tab=ips&user=${u.recharge_code}`}>IP 明细</Button>
                {!u.blocked && <Button type="link" size="small" danger onClick={() => block({ kind: 'user', value: u.recharge_code, reason: `24h 内 ${u.ips} 个 IP` })}>封禁</Button>}
              </Space>
            ),
          },
        ]}
      />
      <h4 style={{ margin: '20px 0 8px' }}>多账号 IP <span className="muted">（可能批量注册薅免费 / 邀请奖励；公司、学校、运营商 NAT 也会出现）</span></h4>
      <Table<SuspectIP>
        rowKey="ip"
        size="small"
        loading={loading}
        dataSource={data?.shared_ips ?? []}
        pagination={{ pageSize: 10, hideOnSinglePage: true }}
        locale={{ emptyText: '没有超过阈值的 IP' }}
        columns={[
          { title: 'IP', render: (_, s) => <IPCell ip={s.ip} /> },
          { title: '类型', width: 70, render: (_, s) => <ClassTag c={s.class} /> },
          { title: '订阅数', dataIndex: 'users', width: 80 },
          {
            title: '账号',
            render: (_, s) => (
              <span className="mono" style={{ fontSize: 12 }}>
                {s.recharge_codes.slice(0, 6).join('、')}
                {s.users > 6 && ` 等 ${s.users} 个`}
              </span>
            ),
          },
          { title: '最近', width: 150, render: (_, s) => dateTime(s.last_seen) },
          {
            title: '操作',
            width: 150,
            render: (_, s) => (
              <Space size={0}>
                <Button type="link" size="small" href={`/admin/risk/?tab=ips&ip=${s.ip}`}>明细</Button>
                {s.blocked ? <Tag color="error">已封禁</Tag> : <Button type="link" size="small" danger onClick={() => block({ kind: 'ip', value: s.ip, reason: `${s.users} 个账号共用` })}>封禁</Button>}
              </Space>
            ),
          },
        ]}
      />
    </>
  )
}

function IPRecords({ initial }: { initial: { user?: string; ip?: string } }) {
  const { message } = App.useApp()
  const [q, setQ] = useState({ user: initial.user ?? '', ip: initial.ip ?? '', source: '', days: 0 })
  const [pager, setPager] = useState({ page: 1, size: 20 })
  const [data, setData] = useState<{ items: IPRecord[]; total: number }>({ items: [], total: 0 })
  const [loading, setLoading] = useState(false)
  const load = useCallback(async () => {
    setLoading(true)
    try {
      setData(await api.risk.ips({ ...q, days: q.days || undefined, page: pager.page, size: pager.size }))
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setLoading(false)
    }
  }, [q, pager, message])
  useEffect(() => {
    load()
  }, [load])
  const block = useBlockDialog(load)
  const set = (p: Partial<typeof q>) => {
    setQ((x) => ({ ...x, ...p }))
    setPager((x) => ({ ...x, page: 1 }))
  }
  return (
    <>
      <div className="toolbar">
        <Input.Search allowClear defaultValue={q.user} placeholder="充值 ID，如 VP-00010" style={{ width: 200 }} onSearch={(v) => set({ user: v.trim() })} />
        <Input.Search allowClear defaultValue={q.ip} placeholder="IP，或前缀如 1.2.3." style={{ width: 200 }} onSearch={(v) => set({ ip: v.trim() })} />
        <Segmented value={q.source} onChange={(v) => set({ source: v as string })} options={[{ label: '全部来源', value: '' }, { label: 'App 接口', value: 'api' }, { label: '节点连接', value: 'node' }]} />
        <Select value={q.days} style={{ width: 120 }} onChange={(v) => set({ days: v })} options={[{ value: 0, label: '全部时间' }, { value: 1, label: '近 24 小时' }, { value: 7, label: '近 7 天' }, { value: 30, label: '近 30 天' }]} />
        <div className="spacer" />
        <Button icon={<ReloadOutlined />} onClick={load}>刷新</Button>
      </div>
      <IPTable
        items={data.items}
        total={data.total}
        loading={loading}
        page={pager.page}
        size={pager.size}
        onPage={(page, size) => setPager({ page, size })}
        onBlock={(ip) => block({ kind: 'ip', value: ip })}
      />
    </>
  )
}

function Rules() {
  const { modal } = App.useApp()
  const { data, loading, reload } = useAsync(api.risk.rules)
  const { run } = useAction()
  const block = useBlockDialog(() => reload(true))
  const remove = (r: RiskRule) =>
    modal.confirm({
      title: '解除封禁',
      content: `解除 ${r.kind === 'ip' ? r.value : r.recharge_code}？`,
      onOk: async () => {
        if (await run(() => api.risk.removeRule(r.kind, r.value), '已解除')) reload(true)
      },
    })
  return (
    <>
      <div className="toolbar">
        <span className="muted">封禁 IP：App 接口返回「网络受限」，节点丢弃来自该地址的流量。封禁用户：无法登录，节点上立即下线（多设备成员随主账号）。</span>
        <div className="spacer" />
        <Button icon={<StopOutlined />} onClick={() => block({ kind: 'ip', value: '' })}>封禁 IP</Button>
        <Button danger icon={<StopOutlined />} onClick={() => block({ kind: 'user', value: '' })}>封禁用户</Button>
      </div>
      <Table<RiskRule>
        rowKey={(r) => r.kind + '|' + r.value}
        size="small"
        loading={loading}
        dataSource={data?.items ?? []}
        pagination={{ pageSize: 20, hideOnSinglePage: true }}
        locale={{ emptyText: '暂无封禁' }}
        columns={[
          { title: '类型', width: 70, render: (_, r) => <Tag bordered={false} color={r.kind === 'ip' ? 'blue' : 'volcano'}>{r.kind === 'ip' ? 'IP' : '用户'}</Tag> },
          { title: '对象', render: (_, r) => <span className="mono">{r.kind === 'ip' ? r.value : r.recharge_code}</span> },
          { title: '原因', dataIndex: 'reason', render: (v) => v || <span className="muted">—</span> },
          { title: '到期', width: 150, render: (_, r) => (r.expires_at ? dateTime(r.expires_at) : '永久') },
          { title: '状态', width: 80, render: (_, r) => (r.active ? <Tag color="error">生效中</Tag> : <Tag>已过期</Tag>) },
          { title: '创建', width: 150, render: (_, r) => dateTime(r.created_at) },
          { title: '', width: 80, render: (_, r) => <Button type="link" size="small" onClick={() => remove(r)}>解除</Button> },
        ]}
      />
    </>
  )
}

function Settings() {
  const { data, setData } = useAsync(api.risk.config)
  const { busy, run } = useAction()
  const [form] = Form.useForm<RiskConfig>()
  useEffect(() => {
    if (data) form.setFieldsValue({ ...data, record_api: data.record_api ?? true, record_node: data.record_node ?? true })
  }, [data, form])
  const save = async () => {
    const v = await form.validateFields()
    await run(async () => setData(await api.risk.saveConfig(v)), '已保存')
  }
  return (
    <Form form={form} layout="vertical" style={{ maxWidth: 520 }}>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="只记录来源 IP（谁从哪里连接），不记录访问了什么网站。记录按「用户 + IP + 来源」聚合，超过保留天数自动删除。"
      />
      <Space size={32}>
        <Form.Item name="record_api" label="记录 App 接口 IP" valuePropName="checked"><Switch /></Form.Item>
        <Form.Item name="record_node" label="记录节点连接 IP" valuePropName="checked" extra="需要节点升级到本版本"><Switch /></Form.Item>
      </Space>
      <Form.Item name="retention_days" label="保留天数" rules={[{ required: true }]}>
        <InputNumber min={1} max={365} addonAfter="天" style={{ width: 180 }} />
      </Form.Item>
      <Form.Item name="max_ips_per_user" label="单账号 24 小时 IP 数阈值" extra="超过列为「多 IP 账号」并打上风险标签；手机网络常换 IP，建议 ≥ 8">
        <InputNumber min={1} max={1000} style={{ width: 180 }} />
      </Form.Item>
      <Form.Item name="max_users_per_ip" label="单 IP 24 小时订阅数阈值" extra="超过列为「多账号 IP」；多设备共享的成员按主账号算一个">
        <InputNumber min={1} max={1000} style={{ width: 180 }} />
      </Form.Item>
      <Button type="primary" loading={busy} onClick={save}>保存</Button>
    </Form>
  )
}

export default function RiskPage() {
  // Deep links from other pages: /admin/risk/?tab=ips&user=VP-00010
  const [params] = useState(() => (typeof window === 'undefined' ? new URLSearchParams() : new URLSearchParams(window.location.search)))
  const [tab, setTab] = useState(params.get('tab') ?? 'suspicious')
  return (
    <div className="page">
      <PageCard>
        <Tabs
          activeKey={tab}
          onChange={setTab}
          items={[
            { key: 'suspicious', label: <span><SafetyOutlined /> 可疑行为</span>, children: <Suspicious /> },
            { key: 'ips', label: 'IP 记录', children: <IPRecords initial={{ user: params.get('user') ?? undefined, ip: params.get('ip') ?? undefined }} /> },
            { key: 'rules', label: '封禁规则', children: <Rules /> },
            { key: 'settings', label: '设置', children: <Settings /> },
          ]}
        />
      </PageCard>
    </div>
  )
}
