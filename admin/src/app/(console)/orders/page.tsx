'use client'
import { ReloadOutlined, SearchOutlined } from '@ant-design/icons'
import { App, Button, Input, Segmented, Table, Tag } from 'antd'
import { PageCard } from '@/components/PageCard'
import { useAction } from '@/hooks/useAsync'
import { usePaged } from '@/hooks/usePaged'
import { api, type Order } from '@/lib/api'
import { dateTime, yuan } from '@/lib/format'

export default function OrdersPage() {
  const { modal } = App.useApp()
  const t = usePaged(api.orders.list, { q: '', status: '' })
  const { run } = useAction()

  const markPaid = (o: Order) =>
    modal.confirm({
      title: '手动入账',
      content: `确认已线下收到 ${yuan(o.amount_cents)}，为 ${o.email || '该用户'} 开通「${o.plan_id}」？`,
      onOk: async () => {
        if (await run(() => api.orders.markPaid(o.id), '已入账并开通套餐')) t.reload()
      },
    })

  return (
    <div className="page">
      <PageCard>
        <div className="toolbar">
          <Input.Search allowClear prefix={<SearchOutlined />} placeholder="订单号 / 充值 ID / 邮箱" style={{ width: 260 }} onSearch={(v) => t.setFilter('q', v)} />
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
        <Table<Order>
          rowKey="id"
          loading={t.loading}
          dataSource={t.items}
          pagination={t.pagination}
          scroll={{ x: 1000 }}
          columns={[
            { title: '订单号', dataIndex: 'id', render: (v) => <span className="mono">{v}</span> },
            {
              title: '用户',
              render: (_, o) => (
                <>
                  <div className="mono" style={{ fontWeight: 600 }}>VP-{String(o.user_id).padStart(5, '0')}</div>
                  {o.email && <div className="muted">{o.email}</div>}
                </>
              ),
            },
            { title: '套餐', dataIndex: 'plan_id', width: 100 },
            { title: '金额', width: 100, render: (_, o) => yuan(o.amount_cents) },
            { title: '支付方式', dataIndex: 'method', width: 110 },
            { title: '状态', width: 90, render: (_, o) => <Tag color={o.status === 'paid' ? 'success' : 'default'}>{o.status === 'paid' ? '已支付' : '待支付'}</Tag> },
            { title: '下单时间', width: 150, render: (_, o) => dateTime(o.created_at) },
            { title: '支付时间', width: 150, render: (_, o) => dateTime(o.paid_at) },
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
