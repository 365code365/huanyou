'use client'
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import { Button, Drawer, Form, Input, InputNumber, Space, Switch, Table, Tag } from 'antd'
import { NumberUnit } from '@/components/NumberUnit'
import { PageCard } from '@/components/PageCard'
import { useCatalog } from '@/hooks/useCatalog'
import { api, type Plan } from '@/lib/api'
import { speed, traffic, yuan } from '@/lib/format'

const blank = (): Plan => ({
  id: '', name: '', description: '', price_cents: 2500, duration_days: 30, traffic_gb: 200,
  speed_mbps: 0, over_quota_kbps: 0, hidden: false, sort: 0,
})
/** Same order as the app: price ascending, then duration, then the manual sort value. */
const byPrice = (list: Plan[]) =>
  [...list].sort((a, b) => a.price_cents - b.price_cents || a.duration_days - b.duration_days || a.sort - b.sort)
// "free" is a response-only flag; never send it back.
const strip = ({ free: _free, ...p }: Plan) => p as Plan

/** Form shows price in yuan; the API stores cents. */
const PriceInput = ({ id, value, onChange }: { id?: string; value?: number; onChange?: (v: number) => void }) => (
  <InputNumber
    id={id}
    min={0.01}
    precision={2}
    prefix="¥"
    style={{ width: 160 }}
    value={value === undefined ? undefined : value / 100}
    onChange={(v) => onChange?.(Math.round((v ?? 0) * 100))}
  />
)

export default function PlansPage() {
  const c = useCatalog(api.plans, blank, '套餐')
  const editingFree = Form.useWatch('free', c.form)

  return (
    <div className="page">
      <PageCard>
        <div className="toolbar">
          <span className="muted">按价格从低到高排列，App 内顺序相同；免费套餐在「系统设置」里指定；下架的套餐不再出售，已购用户不受影响</span>
          <div className="spacer" />
          <Button icon={<ReloadOutlined />} onClick={() => c.load()}>刷新</Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => c.openCreate({ sort: (c.items.at(-1)?.sort ?? 0) + 10 })}>新增套餐</Button>
        </div>
        <Table<Plan>
          rowKey="id"
          loading={c.loading}
          dataSource={byPrice(c.items)}
          pagination={false}
          scroll={{ x: 900 }}
          columns={[
            {
              title: '套餐',
              render: (_, p) => (
                <>
                  <div>
                    {p.name} <span className="muted">{p.id}</span> {p.free && <Tag color="success" bordered={false}>免费套餐</Tag>}
                  </div>
                  <div className="muted">{p.description}</div>
                </>
              ),
            },
            { title: '价格', width: 100, render: (_, p) => (p.free ? '免费' : yuan(p.price_cents)) },
            { title: '时长', width: 90, render: (_, p) => (p.free ? '长期' : `${p.duration_days} 天`) },
            { title: '流量 / 周期', width: 110, render: (_, p) => traffic(p.traffic_gb) },
            { title: '限速', width: 100, render: (_, p) => speed(p.speed_mbps * 1000) },
            { title: '超额后', width: 130, render: (_, p) => (!p.traffic_gb ? '—' : p.over_quota_kbps ? '限速 ' + speed(p.over_quota_kbps) : '停止服务') },
            { title: '同价排序', dataIndex: 'sort', width: 90 },
            { title: '在售', width: 80, render: (_, p) => <Switch checked={!p.hidden} onChange={(v) => c.patch(p, { hidden: !v }, strip)} /> },
            {
              title: '操作',
              width: 120,
              fixed: 'right',
              render: (_, p) => (
                <Space size={0}>
                  <Button type="link" size="small" onClick={() => c.openEdit(p)}>编辑</Button>
                  <Button type="link" size="small" danger onClick={() => c.remove(p)}>删除</Button>
                </Space>
              ),
            },
          ]}
        />
      </PageCard>

      <Drawer
        open={c.open}
        onClose={c.close}
        size={480}
        title={c.mode === 'create' ? '新增套餐' : '编辑套餐'}
        extra={<Space><Button onClick={c.close}>取消</Button><Button type="primary" loading={c.saving} onClick={() => c.save(strip)}>保存</Button></Space>}
      >
        <Form form={c.form} layout="vertical">
          <Form.Item name="free" hidden><Input /></Form.Item>
          <Form.Item name="id" label="ID" rules={[{ required: true, pattern: /^[a-z0-9][a-z0-9_-]{0,31}$/, message: '小写字母 / 数字 / - / _' }]}>
            <Input disabled={c.mode === 'edit'} placeholder="如 month、year" />
          </Form.Item>
          <Form.Item name="name" label="名称" rules={[{ required: true }]}><Input placeholder="如 月付" /></Form.Item>
          <Form.Item name="description" label="描述"><Input.TextArea rows={2} /></Form.Item>
          {!editingFree && (
            <Space size={24}>
              <Form.Item name="price_cents" label="价格" rules={[{ required: true }]}><PriceInput /></Form.Item>
              <Form.Item name="duration_days" label="有效天数" rules={[{ required: true }]}><NumberUnit min={1} unit="天" /></Form.Item>
            </Space>
          )}
          <Space size={24}>
            <Form.Item name="traffic_gb" label="流量 / 周期" extra="0 = 不限"><NumberUnit min={0} precision={2} unit="GB" /></Form.Item>
            <Form.Item name="speed_mbps" label="限速" extra="0 = 不限速"><NumberUnit min={0} unit="Mbps" /></Form.Item>
          </Space>
          <Form.Item noStyle shouldUpdate={(a, b) => a.traffic_gb !== b.traffic_gb}>
            {({ getFieldValue }) =>
              getFieldValue('traffic_gb') > 0 && (
                <Form.Item name="over_quota_kbps" label="超额后速度" extra="流量用完后的限速，0 = 停止服务直到下个周期">
                  <NumberUnit min={0} unit="kbps" />
                </Form.Item>
              )
            }
          </Form.Item>
          <Space size={24}>
            <Form.Item name="sort" label="同价排序" tooltip="套餐按价格从低到高展示；价格和时长都相同时，按此值排序"><InputNumber /></Form.Item>
            <Form.Item name="hidden" label="在售" getValueFromEvent={(v: boolean) => !v} getValueProps={(v: boolean) => ({ checked: !v })}>
              <Switch />
            </Form.Item>
          </Space>
        </Form>
      </Drawer>
    </div>
  )
}
