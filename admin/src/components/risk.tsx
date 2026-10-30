'use client'
import { StopOutlined } from '@ant-design/icons'
import { App, Button, Form, Input, InputNumber, Space, Table, Tag, Tooltip } from 'antd'
import { useState } from 'react'
import { useAction } from '@/hooks/useAsync'
import { api, type IPRecord } from '@/lib/api'
import { dateTime, errMsg } from '@/lib/format'
import { ipClassLabel } from '@/lib/segments'

type BlockDraft = { kind: 'ip' | 'user'; value: string; reason?: string }

/** Shared "封禁" dialog: IP / CIDR or user, with reason and duration. */
export function useBlockDialog(onDone: () => void) {
  const { modal } = App.useApp()
  const { run } = useAction()
  const [form] = Form.useForm()
  return (d: BlockDraft) => {
    form.setFieldsValue({ reason: d.reason ?? '', days: 0, value: d.value })
    modal.confirm({
      title: d.kind === 'ip' ? '封禁 IP' : '封禁用户',
      icon: <StopOutlined style={{ color: '#d64545' }} />,
      width: 460,
      content: (
        <Form form={form} layout="vertical" style={{ marginTop: 12 }}>
          <Form.Item name="value" label={d.kind === 'ip' ? 'IP 或网段' : '用户'} extra={d.kind === 'ip' ? '如 1.2.3.4 或 1.2.3.0/24；节点会在下次同步（约 30 秒内）丢弃来自该地址的流量' : '封禁后 App 无法登录使用，节点上该账号立即下线'}>
            <Input className="mono" />
          </Form.Item>
          <Form.Item name="reason" label="原因"><Input maxLength={200} placeholder="如：共享账号 / 倒卖 / 攻击" /></Form.Item>
          <Form.Item name="days" label="时长" extra="0 为永久">
            <InputNumber min={0} max={3650} addonAfter="天" style={{ width: 160 }} />
          </Form.Item>
        </Form>
      ),
      okText: '封禁',
      okButtonProps: { danger: true },
      onOk: async () => {
        const v = form.getFieldsValue()
        const ok = await run(() => api.risk.addRule({ kind: d.kind, value: String(v.value ?? '').trim(), reason: v.reason ?? '', days: v.days ?? 0 }), '已封禁')
        if (!ok) throw new Error('keep open')
        onDone()
      },
    })
  }
}

export function ClassTag({ c }: { c: string }) {
  const m = ipClassLabel[c]
  return m ? <Tag bordered={false} color={m.color}>{m.label}</Tag> : null
}

/** Clickable IP: looks up the location on demand (external service, admin-initiated only). */
export function IPCell({ ip }: { ip: string }) {
  const { message } = App.useApp()
  const [geo, setGeo] = useState<string>()
  const look = async () => {
    try {
      const g = await api.risk.geo(ip)
      setGeo([g.country, g.region, g.city].filter(Boolean).join(' · ') || '未知')
    } catch (e) {
      message.error(errMsg(e))
    }
  }
  return (
    <Space size={4}>
      <span className="mono">{ip}</span>
      {geo ? <span className="muted">{geo}</span> : (
        <Tooltip title="查询归属地（会把这个 IP 发给第三方查询服务）">
          <Button type="link" size="small" style={{ padding: 0 }} onClick={look}>归属地</Button>
        </Tooltip>
      )}
    </Space>
  )
}

export function IPTable({ items, loading, total, page, size, onPage, onBlock, showUser = true }: {
  items: IPRecord[]
  loading?: boolean
  total?: number
  page?: number
  size?: number
  onPage?: (page: number, size: number) => void
  onBlock?: (ip: string) => void
  showUser?: boolean
}) {
  return (
    <Table<IPRecord>
      rowKey={(r) => `${r.user_id}|${r.ip}|${r.source}|${r.node_id}`}
      size="small"
      loading={loading}
      dataSource={items}
      pagination={onPage ? { current: page, pageSize: size, total, showSizeChanger: true, onChange: onPage, showTotal: (t) => `共 ${t} 条` } : { pageSize: 10, hideOnSinglePage: true }}
      locale={{ emptyText: '暂无 IP 记录' }}
      scroll={{ x: 760 }}
      columns={[
        ...(showUser ? [{ title: '用户', width: 100, render: (_: unknown, r: IPRecord) => <span className="mono">{r.recharge_code}</span> }] : []),
        { title: 'IP', render: (_, r) => <IPCell ip={r.ip} /> },
        { title: '类型', width: 64, render: (_, r) => <ClassTag c={r.class} /> },
        {
          title: '来源',
          width: 120,
          render: (_, r) => (r.source === 'node' ? <Tooltip title="VPN 连接的来源地址"><Tag bordered={false} color="purple">节点 · {r.node_name || r.node_id}</Tag></Tooltip> : <Tooltip title="App 访问接口的地址"><Tag bordered={false}>App 接口</Tag></Tooltip>),
        },
        { title: '次数', dataIndex: 'hits', width: 70 },
        { title: '首次', width: 140, render: (_, r) => dateTime(r.first_seen) },
        { title: '最近', width: 140, render: (_, r) => dateTime(r.last_seen) },
        ...(onBlock
          ? [{
              title: '',
              width: 70,
              render: (_: unknown, r: IPRecord) => (r.blocked ? <Tag color="error">已封禁</Tag> : <Button type="link" size="small" danger onClick={() => onBlock(r.ip)}>封禁</Button>),
            }]
          : []),
      ]}
    />
  )
}

