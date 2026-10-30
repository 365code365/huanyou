'use client'
import { Alert, Button, Descriptions, Form, Input, InputNumber, Select, Space, Spin, Switch, Tag } from 'antd'
import { useEffect } from 'react'
import { PageCard } from '@/components/PageCard'
import { PlanSelect } from '@/components/PlanSelect'
import { useAction, useAsync } from '@/hooks/useAsync'
import { api, type Settings } from '@/lib/api'

export default function SettingsPage() {
  const { data, reload, setData } = useAsync(() => Promise.all([api.settings.get(), api.plans.list()]))
  const [form] = Form.useForm<Settings>()
  const { busy, run } = useAction()
  const referralOn = Form.useWatch(['referral', 'enabled'], form)
  const freePlan = Form.useWatch('free_plan', form)

  useEffect(() => {
    if (data) form.setFieldsValue(data[0].settings)
  }, [data, form])

  if (!data) return <Spin style={{ display: 'block', margin: 80 }} />
  const [{ settings, system: sys }, { items: plans }] = data

  const save = async () => {
    const v = await form.validateFields()
    // Monitor settings are edited on the 监控预警 page; keep them as stored.
    const body: Settings = { ...settings, ...v, free_plan: v.free_plan ?? '', referral: { ...settings.referral, ...v.referral }, share: { ...settings.share, ...v.share }, monitor: settings.monitor }
    let saved: Settings | undefined
    if (await run(async () => (saved = await api.settings.save(body)), '设置已保存，立即生效')) {
      setData([{ settings: saved!, system: sys }, { items: plans, total: plans.length }])
    }
  }

  return (
    <div className="page">
      <Form form={form} labelCol={{ flex: '140px' }} wrapperCol={{ flex: '1' }} style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        <PageCard title="套餐与流量">
          <div style={{ maxWidth: 640 }}>
            <Form.Item name="free_plan" label="免费套餐" extra="没有付费套餐的用户使用它；不选则未付费用户无法连接">
              <PlanSelect plans={plans} allowClear placeholder="不提供免费套餐" />
            </Form.Item>
            <Form.Item name="traffic_period_days" label="流量重置周期" rules={[{ required: true }]}>
              <InputNumber min={1} suffix="天" />
            </Form.Item>
          </div>
        </PageCard>

        <PageCard title="邀请送时长">
          <div style={{ maxWidth: 640 }}>
            <Form.Item name={['referral', 'enabled']} label="启用" valuePropName="checked"><Switch /></Form.Item>
            {referralOn && (
              <>
                <Form.Item name={['referral', 'reward_plan_id']} label="奖励套餐" rules={[{ required: true, message: '请选择付费套餐' }]} extra="奖励按该套餐发放；对方已有同一套餐则顺延">
                  <PlanSelect plans={plans.filter((p) => p.id !== freePlan)} />
                </Form.Item>
                <Form.Item name={['referral', 'inviter_days']} label="邀请人获得"><InputNumber min={0} suffix="天 / 人" /></Form.Item>
                <Form.Item name={['referral', 'invitee_days']} label="被邀请人获得"><InputNumber min={0} suffix="天" /></Form.Item>
                <Form.Item name={['referral', 'max_invites']} label="每人奖励上限" extra="0 = 不限"><InputNumber min={0} suffix="人" /></Form.Item>
              </>
            )}
          </div>
        </PageCard>

        <PageCard title="下载与分享">
          <div style={{ maxWidth: 640 }}>
            <Form.Item name={['share', 'slogan']} label="宣传语" extra="显示在邀请海报和下载页，留空为「一键连接，畅游全球」">
              <Input maxLength={30} placeholder="一键连接，畅游全球" />
            </Form.Item>
            <Form.Item name={['share', 'android_url']} label="Android 下载链接" extra={<>留空则使用服务器上的 APK（{sys.public_url || '对外地址'}/dl/app.apk，放在 download_dir）</>}>
              <Input placeholder="https://..." />
            </Form.Item>
            <Form.Item name={['share', 'ios_url']} label="iPhone 下载链接" extra="App Store / TestFlight / 企业签名地址；留空显示「即将上线」">
              <Input placeholder="https://..." />
            </Form.Item>
            <Form.Item label="下载页">
              {sys.public_url ? <a href={`${sys.public_url}/download`} target="_blank" rel="noreferrer">{sys.public_url}/download</a> : <span className="muted">需要配置对外地址</span>}
            </Form.Item>
          </div>
        </PageCard>
        <PageCard title="AI 应用模式">
          <Form.Item name="ai_domains" label="走代理的域名" extra="App 选择「AI」模式时，只有这些域名（含子域名）走代理，其余直连">
            <Select mode="tags" tokenSeparators={[',', ' ', '\n']} placeholder="输入域名回车添加" />
          </Form.Item>
        </PageCard>

        <PageCard title="系统信息（只读，来自 api.json）">
          {sys.mock_payments && <Alert type="error" showIcon style={{ marginBottom: 12 }} message="模拟支付已开启：任何人都能免费开通套餐，上线前请在 api.json 关闭 payments.mock" />}
          <Descriptions column={2} size="small" bordered>
            <Descriptions.Item label="对外地址">{sys.public_url || '—'}</Descriptions.Item>
            <Descriptions.Item label="开放注册">{sys.allow_register ? '是' : '否'}</Descriptions.Item>
            <Descriptions.Item label="支付方式">{sys.payment_methods.join('、') || '未配置'}</Descriptions.Item>
            <Descriptions.Item label="模拟支付">{sys.mock_payments ? <Tag color="error">已开启</Tag> : '关闭'}</Descriptions.Item>
          </Descriptions>
        </PageCard>
      </Form>

      <div style={{ position: 'sticky', bottom: 0, marginTop: 16, padding: '12px 0', display: 'flex', justifyContent: 'flex-end', background: '#f5f4f0' }}>
        <Space>
          <Button onClick={() => { form.setFieldsValue(settings); reload() }}>撤销修改</Button>
          <Button type="primary" loading={busy} onClick={save}>保存设置</Button>
        </Space>
      </div>
    </div>
  )
}
