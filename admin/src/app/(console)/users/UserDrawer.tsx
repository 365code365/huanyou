'use client'
import { App, Button, DatePicker, Descriptions, Drawer, Form, Input, Space, Spin, Table, Tag, Typography } from 'antd'
import dayjs, { type Dayjs } from 'dayjs'
import { useCallback, useEffect, useState } from 'react'
import { BarChart } from '@/components/BarChart'
import { PlanSelect } from '@/components/PlanSelect'
import { useAction } from '@/hooks/useAsync'
import { api, type Order, type Plan, type UserDetail, type UserPatch } from '@/lib/api'
import { bytes, date, dateTime, errMsg, speed, yuan } from '@/lib/format'
import { colors } from '@/lib/theme'

interface EditForm {
  plan_id?: string
  expires_at?: Dayjs | null
  note: string
  password?: string
}

/** User detail: info, edit form, actions, 30-day usage, orders. */
export function UserDrawer({ id, plans, onClose, onChanged }: { id?: number; plans: Plan[]; onClose: () => void; onChanged: () => void }) {
  const { message, modal } = App.useApp()
  const [d, setD] = useState<UserDetail>()
  const [loading, setLoading] = useState(false)
  const [form] = Form.useForm<EditForm>()
  const { busy, run } = useAction()

  const load = useCallback(async () => {
    if (!id) return
    setLoading(true)
    try {
      const r = await api.users.get(id)
      setD(r)
      form.setFieldsValue({
        plan_id: r.user.plan_id || undefined,
        expires_at: r.user.expires_at ? dayjs(r.user.expires_at) : null,
        note: r.user.note,
        password: '',
      })
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setLoading(false)
    }
  }, [id, form, message])

  useEffect(() => {
    if (id) load()
    else setD(undefined)
  }, [id, load])

  const act = async (fn: () => Promise<unknown>, ok: string) => {
    if (await run(fn, ok)) {
      onChanged()
      load()
    }
  }

  const confirmAct = (title: string, content: string, fn: () => Promise<unknown>, ok: string, danger = false) =>
    modal.confirm({ title, content, okButtonProps: { danger }, onOk: () => act(fn, ok) })

  const save = async () => {
    const v = await form.validateFields()
    if (v.plan_id && !v.expires_at) return message.warning('请选择到期时间')
    const body: UserPatch = {
      note: v.note ?? '',
      plan_id: v.plan_id ?? '',
      expires_at: v.plan_id && v.expires_at ? v.expires_at.toISOString() : '',
    }
    if (v.password) body.password = v.password
    act(() => api.users.patch(id!, body), '已保存')
  }

  const usagePoints = (() => {
    const by = new Map((d?.usage ?? []).map((u) => [u.day, u.up + u.down]))
    return Array.from({ length: 30 }, (_, i) => {
      const key = dayjs().subtract(29 - i, 'day').format('YYYY-MM-DD')
      return { label: key.slice(5), value: by.get(key) ?? 0 }
    })
  })()

  const u = d?.user
  const e = u?.entitlement

  return (
    <Drawer open={!!id} onClose={onClose} size={600} title={u ? (u.email ? `${u.recharge_code} · ${u.email}` : u.recharge_code) : '用户详情'} destroyOnHidden>
      <Spin spinning={loading}>
        {u && e && (
          <>
            <Descriptions column={2} size="small" bordered>
              <Descriptions.Item label="ID">{u.id}</Descriptions.Item>
              <Descriptions.Item label="状态">
                <Tag color={u.disabled ? 'error' : e.can_connect ? 'success' : 'warning'}>
                  {u.disabled ? '已停用' : e.can_connect ? '可连接' : e.reason}
                </Tag>
              </Descriptions.Item>
              <Descriptions.Item label="当前套餐">{e.plan_name || '无'}</Descriptions.Item>
              <Descriptions.Item label="到期">{date(e.expires_at)}</Descriptions.Item>
              <Descriptions.Item label="本周期流量">
                {bytes(e.used_bytes)} / {e.quota_bytes ? bytes(e.quota_bytes) : '不限'}
              </Descriptions.Item>
              <Descriptions.Item label="限速">{speed(e.speed_kbps)}</Descriptions.Item>
              <Descriptions.Item label="邀请码"><span className="mono">{u.ref_code}</span></Descriptions.Item>
              <Descriptions.Item label="已邀请">{d.invited_count} 人</Descriptions.Item>
              <Descriptions.Item label="注册">{dateTime(u.created_at)}</Descriptions.Item>
              <Descriptions.Item label="邀请人">{u.invited_by || '—'}</Descriptions.Item>
              <Descriptions.Item label="UUID" span={2}>
                <Typography.Text className="mono" copyable>{u.uuid}</Typography.Text>
              </Descriptions.Item>
            </Descriptions>

            <Typography.Title level={5} style={{ marginTop: 24 }}>编辑</Typography.Title>
            <Form form={form} labelCol={{ span: 5 }}>
              <Form.Item name="plan_id" label="付费套餐">
                <PlanSelect plans={plans.filter((p) => !p.free)} allowClear placeholder="无（使用免费套餐）" />
              </Form.Item>
              <Form.Item noStyle shouldUpdate={(a, b) => a.plan_id !== b.plan_id}>
                {({ getFieldValue }) => (
                  <Form.Item name="expires_at" label="到期时间">
                    <DatePicker showTime style={{ width: '100%' }} disabled={!getFieldValue('plan_id')} />
                  </Form.Item>
                )}
              </Form.Item>
              <Form.Item name="note" label="备注"><Input /></Form.Item>
              <Form.Item name="password" label="重置密码" rules={[{ min: 8, message: '至少 8 位' }]}>
                <Input.Password placeholder="留空则不修改" autoComplete="new-password" />
              </Form.Item>
              <Form.Item wrapperCol={{ offset: 5 }}>
                <Button type="primary" loading={busy} onClick={save}>保存修改</Button>
              </Form.Item>
            </Form>

            <Typography.Title level={5}>操作</Typography.Title>
            <Space wrap>
              <Button danger={!u.disabled} onClick={() => act(() => api.users.patch(u.id, { disabled: !u.disabled }), u.disabled ? '已启用' : '已停用')}>
                {u.disabled ? '启用账号' : '停用账号'}
              </Button>
              <Button onClick={() => confirmAct('清空流量', '清空该用户本周期的已用流量？', () => api.users.resetTraffic(u.id), '流量已清零')}>清空流量</Button>
              <Button onClick={() => confirmAct('更换 UUID', '更换后该用户需在 App 里重新连接一次，继续？', () => api.users.rotateUUID(u.id), 'UUID 已更换')}>更换 UUID</Button>
              <Button
                danger
                type="primary"
                ghost
                onClick={() =>
                  modal.confirm({
                    title: '删除用户',
                    content: `删除 ${u.email || u.recharge_code}？订单和流量记录也会一起删除，且不可恢复。`,
                    okText: '删除',
                    okButtonProps: { danger: true },
                    onOk: async () => {
                      if (await run(() => api.users.remove(u.id), '已删除')) {
                        onClose()
                        onChanged()
                      }
                    },
                  })
                }
              >
                删除用户
              </Button>
            </Space>

            <Typography.Title level={5} style={{ marginTop: 24 }}>近 30 天流量</Typography.Title>
            <BarChart points={usagePoints} color={colors.violet} format={bytes} height={110} />

            <Typography.Title level={5} style={{ marginTop: 24 }}>订单</Typography.Title>
            <Table<Order>
              rowKey="id"
              size="small"
              pagination={false}
              dataSource={d.orders}
              locale={{ emptyText: '暂无订单' }}
              columns={[
                { title: '订单号', dataIndex: 'id', render: (v) => <span className="mono">{v}</span> },
                { title: '套餐', dataIndex: 'plan_id', width: 80 },
                { title: '金额', width: 80, render: (_, o) => yuan(o.amount_cents) },
                { title: '状态', width: 80, render: (_, o) => <Tag color={o.status === 'paid' ? 'success' : 'default'}>{o.status === 'paid' ? '已支付' : '待支付'}</Tag> },
              ]}
            />
          </>
        )}
      </Spin>
    </Drawer>
  )
}
