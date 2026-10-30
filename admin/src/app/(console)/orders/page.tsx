'use client'
import { InfoCircleOutlined, ReloadOutlined, SearchOutlined } from '@ant-design/icons'
import { App, Button, Input, Segmented, Space, Table, Tag, Tooltip, Typography } from 'antd'
import { PageCard } from '@/components/PageCard'
import { useAction, useAsync } from '@/hooks/useAsync'
import { usePaged } from '@/hooks/usePaged'
import { api, type Order } from '@/lib/api'
import { dateTime, yuan } from '@/lib/format'

/** "cashier.alipay" -> 收银台 · 支付宝 */
const methodLabel = (m: string) => {
  const [prov, ch] = m.includes('.') ? m.split('.', 2) : [m, '']
  const chName: Record<string, string> = { alipay: '支付宝', wxpay: '微信支付', qqpay: 'QQ 钱包' }
  const provName: Record<string, string> = { cashier: '收银台', epay: '易支付', alipay: '支付宝官方', mock: '模拟支付' }
  const p = provName[prov] ?? prov
  return ch ? `${p} · ${chName[ch] ?? ch}` : p
}

const { Text } = Typography

export default function OrdersPage() {
  const { modal } = App.useApp()
  const t = usePaged(api.orders.list, { q: '', status: '' })
  const { data: planPage } = useAsync(api.plans.list)
  const planName = (id: string) => planPage?.items.find((p) => p.id === id)?.name ?? id
  const { run } = useAction()

  const markPaid = (o: Order) =>
    modal.confirm({
      title: '手动入账',
      content: `确认已线下收到 ${yuan(o.amount_cents)}，为 VP-${String(o.user_id).padStart(5, '0')} 开通「${planName(o.plan_id)}」？支付订单号将记为 MANUAL。`,
      onOk: async () => {
        if (await run(() => api.orders.markPaid(o.id), '已入账并开通套餐')) t.reload()
      },
    })

  return (
    <div className="page">
      <PageCard>
        <div className="toolbar">
          <Input.Search
            allowClear
            prefix={<SearchOutlined />}
            placeholder="系统订单号 / 支付订单号 / 充值 ID / 邮箱"
            style={{ width: 320 }}
            onSearch={(v) => t.setFilter('q', v)}
          />
          <Segmented
            value={t.filters.status}
            onChange={(v) => t.setFilter('status', v as string)}
            options={[
              { label: '全部', value: '' },
              { label: '已支付', value: 'paid' },
              { label: '待支付', value: 'pending' },
            ]}
          />
          <div className="spacer" />
          <Button icon={<ReloadOutlined />} onClick={t.reload}>刷新</Button>
        </div>
        <div className="muted" style={{ marginBottom: 12 }}>
          <InfoCircleOutlined /> <b>系统订单号</b>：本系统生成（VP 开头），发给收银台的 out_trade_no；
          <b> 支付订单号</b>：收银台 / 支付渠道回调返回的流水号（trade_no），用于和支付后台对账。MANUAL = 后台手动入账。
        </div>
        <Table<Order>
          rowKey="id"
          loading={t.loading}
          dataSource={t.items}
          pagination={t.pagination}
          scroll={{ x: 1250 }}
          columns={[
            {
              title: '系统订单号',
              width: 230,
              render: (_, o) => <Text className="mono" copyable={{ text: o.id }}>{o.id}</Text>,
            },
            {
              title: (
                <Tooltip title="支付渠道返回的交易流水号，支付成功后才有">
                  支付订单号 <InfoCircleOutlined />
                </Tooltip>
              ),
              width: 220,
              render: (_, o) =>
                !o.payment_no ? (
                  <span className="muted">—</span>
                ) : o.payment_no === 'MANUAL' ? (
                  <Tag color="warning" bordered={false}>后台手动入账</Tag>
                ) : (
                  <Text className="mono" copyable={{ text: o.payment_no }}>{o.payment_no}</Text>
                ),
            },
            {
              title: '用户',
              width: 160,
              render: (_, o) => (
                <>
                  <div className="mono" style={{ fontWeight: 600 }}>VP-{String(o.user_id).padStart(5, '0')}</div>
                  {o.email && <div className="muted">{o.email}</div>}
                </>
              ),
            },
            { title: '套餐', width: 110, render: (_, o) => planName(o.plan_id) },
            { title: '金额', width: 90, render: (_, o) => <b>{yuan(o.amount_cents)}</b> },
            { title: '支付方式', width: 150, render: (_, o) => methodLabel(o.method) },
            {
              title: '状态',
              width: 90,
              render: (_, o) => <Tag color={o.status === 'paid' ? 'success' : 'default'}>{o.status === 'paid' ? '已支付' : '待支付'}</Tag>,
            },
            {
              title: '时间',
              width: 180,
              render: (_, o) => (
                <Space direction="vertical" size={0}>
                  <span><span className="muted">下单 </span>{dateTime(o.created_at)}</span>
                  {o.paid_at && <span><span className="muted">支付 </span>{dateTime(o.paid_at)}</span>}
                </Space>
              ),
            },
            {
              title: '操作',
              width: 100,
              fixed: 'right',
              render: (_, o) => o.status === 'pending' && <Button type="link" size="small" onClick={() => markPaid(o)}>手动入账</Button>,
            },
          ]}
        />
      </PageCard>
    </div>
  )
}
