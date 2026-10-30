'use client'
import { PlusOutlined, ReloadOutlined, SearchOutlined } from '@ant-design/icons'
import { App, Button, Form, Input, InputNumber, Modal, Progress, Segmented, Select, Space, Table, Tag } from 'antd'
import { useState } from 'react'
import { PageCard } from '@/components/PageCard'
import { PlanSelect } from '@/components/PlanSelect'
import { useAction, useAsync } from '@/hooks/useAsync'
import { usePaged } from '@/hooks/usePaged'
import { api, type User } from '@/lib/api'
import { bytes, date } from '@/lib/format'
import { segmentMeta } from '@/lib/segments'
import { UserDrawer } from './UserDrawer'

const platformName = (p: string) => (p === 'ios' ? 'iPhone' : p === 'android' ? 'Android' : '未知')
/** How to refer to a user in dialogs: recharge ID, plus email if bound. */
export const who = (u: User) => (u.email ? `${u.recharge_code}（${u.email}）` : u.recharge_code)

export default function UsersPage() {
  const { modal } = App.useApp()
  const t = usePaged(api.users.list, { q: '', status: '', segment: '' })
  const { data: planPage } = useAsync(api.plans.list)
  const plans = planPage?.items ?? []
  const paidPlans = plans.filter((p) => !p.free)
  const { busy, run } = useAction()

  const [detailId, setDetailId] = useState<number>()
  const [createOpen, setCreateOpen] = useState(false)
  const [createForm] = Form.useForm()
  const [grantUser, setGrantUser] = useState<User>()
  const [grantForm] = Form.useForm()

  const create = async () => {
    const v = await createForm.validateFields()
    if (await run(() => api.users.create({ ...v, plan_id: v.plan_id || undefined }), '用户已创建')) {
      setCreateOpen(false)
      t.reload()
    }
  }

  const openGrant = (u: User) => {
    setGrantUser(u)
    grantForm.setFieldsValue({ plan_id: (u.entitlement.paid && u.entitlement.plan_id) || paidPlans[0]?.id, days: 30 })
  }
  const grant = async () => {
    const v = await grantForm.validateFields()
    if (await run(() => api.users.grant(grantUser!.id, v.plan_id, v.days), '已开通')) {
      setGrantUser(undefined)
      t.reload()
    }
  }

  const toggleDisabled = (u: User) =>
    modal.confirm({
      title: u.disabled ? '启用账号' : '停用账号',
      content: u.disabled ? `启用 ${who(u)}？` : `停用 ${who(u)}？节点会在 30 秒内断开该用户。`,
      okButtonProps: { danger: !u.disabled },
      onOk: async () => {
        if (await run(() => api.users.patch(u.id, { disabled: !u.disabled }), u.disabled ? '已启用' : '已停用')) t.reload()
      },
    })

  return (
    <div className="page">
      <PageCard>
        <div className="toolbar">
          <Input.Search
            allowClear
            prefix={<SearchOutlined />}
            placeholder="充值 ID / 邮箱 / 邀请码"
            style={{ width: 280 }}
            onSearch={(v) => t.setFilter('q', v)}
          />
          <Segmented
            value={t.filters.status}
            onChange={(v) => t.setFilter('status', v as string)}
            options={[
              { label: '全部', value: '' },
              { label: '付费', value: 'paid' },
              { label: '免费', value: 'free' },
              { label: '已停用', value: 'disabled' },
            ]}
          />
          <Select
            allowClear
            placeholder="用户分群"
            style={{ width: 140 }}
            value={t.filters.segment || undefined}
            onChange={(v) => t.setFilter('segment', v ?? '')}
            options={Object.entries(segmentMeta).map(([value, m]) => ({ value, label: m.label }))}
          />
          <div className="spacer" />
          <Button icon={<ReloadOutlined />} onClick={t.reload}>刷新</Button>
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => {
              createForm.resetFields()
              createForm.setFieldsValue({ days: 30 })
              setCreateOpen(true)
            }}
          >
            新增用户
          </Button>
        </div>

        <Table<User>
          rowKey="id"
          className="clickable-rows"
          loading={t.loading}
          dataSource={t.items}
          pagination={t.pagination}
          scroll={{ x: 1170 }}
          onRow={(u) => ({ onClick: () => setDetailId(u.id) })}
          columns={[
            {
              title: '充值 ID',
              width: 130,
              render: (_, u) => <span className="mono" style={{ fontSize: 13, fontWeight: 600 }}>{u.recharge_code}</span>,
            },
            {
              title: '账号',
              render: (_, u) => (
                <>
                  <div>{u.email || <span className="muted">设备账号 · {platformName(u.platform)}</span>}</div>
                  {u.note && <div className="muted">{u.note}</div>}
                </>
              ),
            },
            {
              title: '套餐',
              width: 160,
              render: (_, u) =>
                u.disabled ? (
                  <Tag color="error">已停用</Tag>
                ) : (
                  <>
                    <Tag color={u.entitlement.paid ? 'success' : 'default'}>{u.entitlement.plan_name || '无套餐'}</Tag>
                    {u.entitlement.paid && <div className="muted">{date(u.entitlement.expires_at)} 到期</div>}
                  </>
                ),
            },
            {
              title: '本周期流量',
              width: 200,
              render: (_, u) => {
                const e = u.entitlement
                return (
                  <>
                    {e.quota_bytes > 0 && (
                      <Progress
                        size="small"
                        percent={Math.min(100, Math.round((e.used_bytes / e.quota_bytes) * 100))}
                        status={e.throttled || !e.can_connect ? 'exception' : 'normal'}
                      />
                    )}
                    <span className="muted">
                      {bytes(e.used_bytes)} / {e.quota_bytes ? bytes(e.quota_bytes) : '不限'}
                    </span>
                  </>
                )
              },
            },
            {
              title: '分群',
              width: 170,
              render: (_, u) => (
                <Space size={[2, 2]} wrap>
                  {u.is_blocked && <Tag color="error">已封禁</Tag>}
                  {[u.tier, ...(u.tags ?? [])].filter((k) => k && k !== 'free').slice(0, 3).map((k) => (
                    <Tag key={k} bordered={false} color={segmentMeta[k]?.color}>{segmentMeta[k]?.label ?? k}</Tag>
                  ))}
                </Space>
              ),
            },
            { title: '邀请码', dataIndex: 'ref_code', width: 110, render: (v) => <span className="mono">{v}</span> },
            { title: '创建时间', width: 120, render: (_, u) => date(u.created_at) },
            {
              title: '操作',
              width: 200,
              fixed: 'right',
              render: (_, u) => (
                <Space size={0} onClick={(e) => e.stopPropagation()}>
                  <Button type="link" size="small" onClick={() => openGrant(u)}>开通套餐</Button>
                  <Button type="link" size="small" danger={!u.disabled} onClick={() => toggleDisabled(u)}>
                    {u.disabled ? '启用' : '停用'}
                  </Button>
                  <Button type="link" size="small" onClick={() => setDetailId(u.id)}>详情</Button>
                </Space>
              ),
            },
          ]}
        />
      </PageCard>

      <Modal title="新增用户" open={createOpen} onOk={create} confirmLoading={busy} onCancel={() => setCreateOpen(false)} okText="创建" destroyOnHidden>
        <Form form={createForm} layout="vertical" preserve={false}>
          <Form.Item name="email" label="邮箱" rules={[{ required: true, type: 'email', message: '请输入有效邮箱' }]}>
            <Input />
          </Form.Item>
          <Form.Item name="password" label="密码" rules={[{ required: true, min: 8, message: '至少 8 位' }]}>
            <Input.Password />
          </Form.Item>
          <Form.Item name="plan_id" label="套餐">
            <PlanSelect plans={paidPlans} allowClear placeholder="不开通（免费用户）" />
          </Form.Item>
          <Form.Item noStyle shouldUpdate={(a, b) => a.plan_id !== b.plan_id}>
            {({ getFieldValue }) =>
              getFieldValue('plan_id') && (
                <Form.Item name="days" label="天数" rules={[{ required: true }]}>
                  <InputNumber min={1} style={{ width: 160 }} />
                </Form.Item>
              )
            }
          </Form.Item>
          <Form.Item name="note" label="备注">
            <Input />
          </Form.Item>
        </Form>
      </Modal>

      <Modal title="开通 / 续费套餐" open={!!grantUser} onOk={grant} confirmLoading={busy} onCancel={() => setGrantUser(undefined)} okText="确定">
        <p className="muted">{grantUser && who(grantUser)} · 同一套餐顺延；换套餐立即替换并重置流量周期</p>
        <Form form={grantForm} layout="vertical">
          <Form.Item name="plan_id" label="套餐" rules={[{ required: true, message: '请选择套餐' }]}>
            <PlanSelect plans={paidPlans} />
          </Form.Item>
          <Form.Item label="天数" required>
            <Space>
              <Form.Item name="days" noStyle rules={[{ required: true }]}>
                <InputNumber min={1} style={{ width: 120 }} />
              </Form.Item>
              {[7, 30, 90, 365].map((d) => (
                <Button key={d} size="small" onClick={() => grantForm.setFieldValue('days', d)}>{d}</Button>
              ))}
            </Space>
          </Form.Item>
        </Form>
      </Modal>

      <UserDrawer id={detailId} plans={plans} onClose={() => setDetailId(undefined)} onChanged={t.reload} />
    </div>
  )
}
