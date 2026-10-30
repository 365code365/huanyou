'use client'
import { CopyOutlined, FileTextOutlined, KeyOutlined, LinkOutlined, PictureOutlined, PlusOutlined, ReloadOutlined, SafetyOutlined } from '@ant-design/icons'
import {
  Alert, App, Button, DatePicker, Descriptions, Drawer, Form, Input, InputNumber, Popconfirm, Segmented, Select, Space, Spin, Switch, Table, Tabs, Tag, Typography, Upload,
} from 'antd'
import dayjs from 'dayjs'
import { useEffect, useState } from 'react'
import { Markdown } from '@/components/Markdown'
import { NumberUnit } from '@/components/NumberUnit'
import { PageCard } from '@/components/PageCard'
import { PlanSelect } from '@/components/PlanSelect'
import { useAction, useAsync } from '@/hooks/useAsync'
import { api, type Ad, type AdsSettings, type GuardSettings, type PaymentSettings, type Plan, type Settings, type SystemInfo } from '@/lib/api'
import { errMsg } from '@/lib/format'

type Loaded = Awaited<ReturnType<typeof api.settings.get>>

const narrow = { maxWidth: 680 }

/** Settings grouped into tabs; each tab saves on its own. */
export default function SettingsPage() {
  const { data, reload, setData } = useAsync(() => Promise.all([api.settings.get(), api.plans.list()]))
  const [tab, setTab] = useState(() => (typeof window === 'undefined' ? 'basic' : new URLSearchParams(location.search).get('tab') ?? 'basic'))
  if (!data) return <Spin style={{ display: 'block', margin: 80 }} />
  const [loaded, { items: plans }] = data
  const patch = (p: Partial<Loaded>) => setData([{ ...loaded, ...p }, data[1]])
  return (
    <div className="page">
      <PageCard styles={{ body: { paddingTop: 4 } }}>
        <Tabs
          activeKey={tab}
          onChange={(k) => {
            setTab(k)
            history.replaceState(null, '', `?tab=${k}`)
          }}
          items={[
            { key: 'basic', label: '基础', children: <MainForm part="basic" loaded={loaded} plans={plans} onSaved={(s) => patch({ settings: s })} onReset={reload} /> },
            { key: 'referral', label: '邀请奖励', children: <MainForm part="referral" loaded={loaded} plans={plans} onSaved={(s) => patch({ settings: s })} onReset={reload} /> },
            { key: 'share', label: '下载与分享', children: <MainForm part="share" loaded={loaded} plans={plans} onSaved={(s) => patch({ settings: s })} onReset={reload} /> },
            { key: 'payment', label: '支付', children: <PaymentTab loaded={loaded} onSaved={(payment) => patch({ payment })} /> },
            { key: 'ads', label: '广告与弹窗', children: <AdsTab value={loaded.ads} onSaved={(ads) => patch({ ads })} /> },
            { key: 'guard', label: '安全防护', children: <GuardTab value={loaded.guard} onSaved={(guard) => patch({ guard })} /> },
            { key: 'system', label: '系统信息', children: <SystemInfoView sys={loaded.system} /> },
          ]}
        />
      </PageCard>
    </div>
  )
}

function SaveBar({ busy, onSave, onReset }: { busy: boolean; onSave: () => void; onReset?: () => void }) {
  return (
    <Space style={{ marginTop: 8 }}>
      {onReset && <Button onClick={onReset}>撤销修改</Button>}
      <Button type="primary" loading={busy} onClick={onSave}>保存</Button>
    </Space>
  )
}

// ---- basic / referral / share: the main settings object ----

function MainForm({ part, loaded, plans, onSaved, onReset }: {
  part: 'basic' | 'referral' | 'share'
  loaded: Loaded
  plans: Plan[]
  onSaved: (s: Settings) => void
  onReset: () => void
}) {
  const settings = loaded.settings
  const sys = loaded.system
  const [form] = Form.useForm<Settings>()
  const { busy, run } = useAction()
  const referralOn = Form.useWatch(['referral', 'enabled'], form)
  const freePlan = Form.useWatch('free_plan', form) ?? settings.free_plan
  useEffect(() => form.setFieldsValue(settings), [settings, form])
  const save = async () => {
    const v = await form.validateFields()
    const body: Settings = {
      ...settings, ...v, free_plan: part === 'basic' ? v.free_plan ?? '' : settings.free_plan,
      referral: { ...settings.referral, ...v.referral }, share: { ...settings.share, ...v.share },
      multi_device: { ...settings.multi_device, ...v.multi_device }, monitor: settings.monitor,
    }
    let saved: Settings | undefined
    if (await run(async () => (saved = await api.settings.save(body)), '已保存，立即生效')) onSaved(saved!)
  }
  return (
    <Form form={form} labelCol={{ flex: '150px' }} wrapperCol={{ flex: '1' }} style={narrow}>
      {part === 'basic' && (
        <>
          <h4 className="form-section">套餐与流量</h4>
          <Form.Item name="free_plan" label="免费套餐" extra="没有付费套餐的用户使用它；不选则未付费用户无法连接">
            <PlanSelect plans={plans} allowClear placeholder="不提供免费套餐" />
          </Form.Item>
          <Form.Item name="traffic_period_days" label="流量重置周期" rules={[{ required: true }]}>
            <NumberUnit min={1} unit="天" />
          </Form.Item>
          <h4 className="form-section">多设备共享</h4>
          <Form.Item name={['multi_device', 'max_devices']} label="每个订阅设备数" extra="含主设备；会员在 App「我的 → 多设备」生成令牌 / 二维码">
            <NumberUnit min={1} max={20} unit="台" />
          </Form.Item>
          <Form.Item name={['multi_device', 'min_plan_days']} label="套餐最短时长" extra="只有有效期不少于这个天数的付费套餐能共享">
            <NumberUnit min={1} unit="天" />
          </Form.Item>
          <h4 className="form-section">AI 优先模式</h4>
          <Form.Item name="ai_domains" label="走代理的域名" extra="App 选「AI 优先」时只有这些域名（含子域名）走代理，并自动使用最稳定的节点">
            <Select mode="tags" tokenSeparators={[',', ' ', '\n']} placeholder="输入域名回车添加" />
          </Form.Item>
        </>
      )}
      {part === 'referral' && (
        <>
          <Form.Item name={['referral', 'enabled']} label="启用邀请奖励" valuePropName="checked"><Switch /></Form.Item>
          {referralOn && (
            <>
              <Form.Item name={['referral', 'reward_plan_id']} label="奖励套餐" rules={[{ required: true, message: '请选择付费套餐' }]} extra="奖励按该套餐发放；对方已有同一套餐则顺延">
                <PlanSelect plans={plans.filter((p) => p.id !== freePlan)} />
              </Form.Item>
              <Form.Item name={['referral', 'inviter_days']} label="邀请人获得"><NumberUnit min={0} unit="天 / 人" /></Form.Item>
              <Form.Item name={['referral', 'invitee_days']} label="被邀请人获得"><NumberUnit min={0} unit="天" /></Form.Item>
              <Form.Item name={['referral', 'max_invites']} label="每人奖励上限" extra="0 = 不限"><NumberUnit min={0} unit="人" /></Form.Item>
            </>
          )}
        </>
      )}
      {part === 'share' && (
        <>
          <Form.Item name={['share', 'slogan']} label="宣传语" extra="显示在邀请海报和下载页">
            <Input maxLength={30} placeholder="一键连接，畅游全球" />
          </Form.Item>
          <Form.Item name={['share', 'android_url']} label="Android 下载链接" extra="留空则使用「版本发布」里最新的安装包">
            <Input placeholder="https://..." />
          </Form.Item>
          <Form.Item name={['share', 'ios_url']} label="iPhone 下载链接" extra="App Store / TestFlight / 企业签名地址">
            <Input placeholder="https://..." />
          </Form.Item>
          <Form.Item label="下载页">
            {sys.public_url ? <a href={`${sys.public_url}/download`} target="_blank" rel="noreferrer">{sys.public_url}/download</a> : <span className="muted">需要配置对外地址</span>}
          </Form.Item>
        </>
      )}
      <Form.Item label=" " colon={false}>
        <SaveBar busy={busy} onSave={save} onReset={() => { form.setFieldsValue(settings); onReset() }} />
      </Form.Item>
    </Form>
  )
}

// ---- payment ----

function PaymentTab({ loaded, onSaved }: { loaded: Loaded; onSaved: (p: PaymentSettings) => void }) {
  const { message, modal } = App.useApp()
  const [form] = Form.useForm<PaymentSettings>()
  const { busy, run } = useAction()
  const [doc, setDoc] = useState<string>()
  const disabled = Form.useWatch('disabled', form)
  const gwOn = Form.useWatch(['gateway', 'enabled'], form)
  const mid = Form.useWatch(['gateway', 'merchant_id'], form)
  useEffect(() => form.setFieldsValue(loaded.payment), [loaded.payment, form])
  const ep = loaded.endpoints
  const save = async () => {
    const v = await form.validateFields()
    const body: PaymentSettings = { ...loaded.payment, ...v, gateway: { ...loaded.payment.gateway, ...v.gateway } }
    let saved: PaymentSettings | undefined
    if (await run(async () => (saved = await api.settings.saveSection('payment', body)), '支付设置已保存')) onSaved(saved!)
  }
  const genKeys = () => {
    const go = async () => {
      const k = await api.payment.keys()
      form.setFieldsValue({ gateway: { ...form.getFieldValue('gateway'), ...k } })
      message.success('已生成新的商户号和密钥，保存后生效；请把密钥安全地交给支付中心')
    }
    if (mid) modal.confirm({ title: '重新生成密钥', content: '保存后旧密钥立即失效，支付中心需要同步更新，否则通知会验签失败。继续？', onOk: go })
    else go()
  }
  const copy = (v: string) => navigator.clipboard.writeText(v).then(() => message.success('已复制'))
  const testLink = async () => {
    try {
      const r = await api.payment.testLink()
      window.open(r.url, '_blank', 'noopener')
    } catch (e) {
      message.error(errMsg(e))
    }
  }
  return (
    <>
      <Form form={form} labelCol={{ flex: '150px' }} wrapperCol={{ flex: '1' }} style={narrow}>
        <h4 className="form-section">购买开关</h4>
        <Form.Item name="disabled" label="开放购买" getValueFromEvent={(v: boolean) => !v} getValueProps={(v: boolean) => ({ checked: !v })}
          extra="关闭后 App 的购买按钮变灰并显示下方提示，接口也拒绝创建订单；已付款订单的回调仍会正常处理">
          <Switch />
        </Form.Item>
        {disabled && (
          <Form.Item name="disabled_notice" label="关闭时提示">
            <Input maxLength={30} placeholder="暂不支持购买" />
          </Form.Item>
        )}
        <h4 className="form-section">支付中心（下单链接）</h4>
        <Form.Item name={['gateway', 'enabled']} label="启用" valuePropName="checked"><Switch /></Form.Item>
        <Form.Item name={['gateway', 'name']} label="App 显示名称"><Input maxLength={20} placeholder="在线支付" /></Form.Item>
        <Form.Item name={['gateway', 'pay_link']} label="下单链接" rules={[{ required: gwOn, message: '请填写支付中心的收银台地址' }, { pattern: /^https:\/\//, message: '必须是 https 地址' }]}
          extra="用户下单后打开：下单链接?out_trade_no=订单号&amount=金额（分）&…&sign=签名">
          <Input placeholder="https://pay.example.com/checkout" />
        </Form.Item>
        <Form.Item label="商户号与密钥" required={gwOn}>
          <Space direction="vertical" style={{ width: '100%' }}>
            <Button icon={<KeyOutlined />} onClick={genKeys}>{mid ? '重新生成密钥' : '生成商户号和密钥'}</Button>
            <Form.Item name={['gateway', 'merchant_id']} noStyle><Input addonBefore="merchant_id" readOnly className="mono" suffix={mid && <CopyOutlined onClick={() => copy(mid)} />} /></Form.Item>
            <Form.Item name={['gateway', 'sign_key']} noStyle><Input.Password addonBefore="sign_key" readOnly className="mono" /></Form.Item>
            <Form.Item name={['gateway', 'enc_key']} noStyle><Input.Password addonBefore="enc_key" readOnly className="mono" /></Form.Item>
            <span className="muted">签名 HMAC-SHA256，加密 AES-256-GCM。密钥只交给支付中心，不会出现在 App 和链接里。</span>
          </Space>
        </Form.Item>
        <Form.Item label="固定接口">
          <Descriptions size="small" column={1} bordered>
            <Descriptions.Item label="通知"><Typography.Text className="mono" copyable>{ep.notify}</Typography.Text></Descriptions.Item>
            <Descriptions.Item label="查询"><Typography.Text className="mono" copyable>{ep.query}</Typography.Text></Descriptions.Item>
          </Descriptions>
        </Form.Item>
        <Form.Item name={['gateway', 'allow_ips']} label="调用 IP 白名单" extra="可选：只允许支付中心的服务器调用通知 / 查询接口（IP 或网段），留空不限制">
          <Select mode="tags" tokenSeparators={[',', ' ']} placeholder="如 203.0.113.10" />
        </Form.Item>
        <Form.Item label=" " colon={false}>
          <Space wrap>
            <SaveBar busy={busy} onSave={save} />
            <Button icon={<FileTextOutlined />} onClick={async () => setDoc((await api.payment.doc()).markdown)}>查看对接文档</Button>
            <Button icon={<LinkOutlined />} onClick={testLink}>生成测试链接</Button>
          </Space>
        </Form.Item>
      </Form>
      {loaded.system.payment_methods.length > 0 && (
        <Alert type="info" showIcon style={{ ...narrow, marginTop: 8 }} message={`api.json 里还配置了：${loaded.system.payment_methods.join('、')}（同样受「开放购买」开关控制）`} />
      )}
      <Drawer open={!!doc} onClose={() => setDoc(undefined)} size={760} title="支付中心对接文档"
        extra={<Button icon={<CopyOutlined />} onClick={() => doc && copy(doc)}>复制 Markdown</Button>}>
        {doc && <Markdown text={doc} />}
      </Drawer>
    </>
  )
}

// ---- ads ----

const newAd = (slot: Ad['slot']): Ad => ({ id: 'ad' + Date.now().toString(36), slot, title: '', text: '', image: '', link: '', enabled: true, sort: 0, start_at: null, end_at: null })

function AdsTab({ value, onSaved }: { value: AdsSettings; onSaved: (a: AdsSettings) => void }) {
  const { message } = App.useApp()
  const [form] = Form.useForm<AdsSettings>()
  const [items, setItems] = useState<Ad[]>(value.items ?? [])
  const [edit, setEdit] = useState<Ad>()
  const [adForm] = Form.useForm<Ad>()
  const { busy, run } = useAction()
  const adImage = Form.useWatch('image', adForm)
  const adSlot = Form.useWatch('slot', adForm)
  useEffect(() => {
    form.setFieldsValue(value)
    setItems(value.items ?? [])
  }, [value, form])
  const save = async (list = items) => {
    const v = await form.validateFields()
    let saved: AdsSettings | undefined
    if (await run(async () => (saved = await api.settings.saveSection('ads', { ...value, ...v, items: list })), '广告设置已保存，App 下次打开生效')) onSaved(saved!)
  }
  const openAd = (a: Ad) => {
    setEdit(a)
    adForm.resetFields()
    adForm.setFieldsValue(a)
  }
  const saveAd = async () => {
    const v = await adForm.validateFields()
    const ad = { ...edit!, ...v }
    const next = items.some((x) => x.id === ad.id) ? items.map((x) => (x.id === ad.id ? ad : x)) : [...items, ad]
    setItems(next)
    setEdit(undefined)
    await save(next)
  }
  const uploadImage = async (file: File) => {
    const fd = new FormData()
    fd.append('file', file)
    try {
      const r = await api.ads.upload(fd)
      adForm.setFieldValue('image', r.url)
      message.success('图片已上传')
    } catch (e) {
      message.error(errMsg(e))
    }
    return false
  }
  const toISO = { getValueProps: (v?: string | null) => ({ value: v ? dayjs(v) : null }), normalize: (d?: dayjs.Dayjs | null) => (d ? d.second(0).toISOString() : null) }
  const preview = (src: string) => (src.startsWith('/') ? src : src)
  return (
    <>
      <Form form={form} layout="inline" style={{ marginBottom: 16, rowGap: 12 }}>
        <Form.Item name="home_enabled" label="首页横幅" valuePropName="checked"><Switch /></Form.Item>
        <Form.Item name="home_interval" label="轮播间隔"><NumberUnit min={2} max={60} unit="秒" width={90} /></Form.Item>
        <Form.Item name="popup_enabled" label="开屏弹窗 / 通知" valuePropName="checked"><Switch /></Form.Item>
        <Form.Item name="popup_frequency" label="弹出频率">
          <Segmented options={[{ label: '每次打开', value: 'every_launch' }, { label: '每天一次', value: 'daily' }, { label: '只弹一次', value: 'once' }]} />
        </Form.Item>
        <Form.Item><Button type="primary" loading={busy} onClick={() => save()}>保存开关</Button></Form.Item>
      </Form>
      <Alert type="info" showIcon style={{ marginBottom: 12 }}
        message="首页横幅：一张时单张展示，多张自动轮播；点击跳转到链接（浏览器打开）。开屏弹窗：取排序最前、在投放时间内的一条，可只有文字作为通知。两个开关默认关闭。" />
      <div className="toolbar">
        <Button icon={<PlusOutlined />} onClick={() => openAd(newAd('home'))}>添加首页横幅</Button>
        <Button icon={<PlusOutlined />} onClick={() => openAd(newAd('popup'))}>添加开屏弹窗</Button>
      </div>
      <Table<Ad>
        rowKey="id"
        size="small"
        dataSource={[...items].sort((a, b) => (a.slot === b.slot ? a.sort - b.sort : a.slot.localeCompare(b.slot)))}
        pagination={false}
        locale={{ emptyText: '还没有广告' }}
        columns={[
          { title: '位置', width: 90, render: (_, a) => <Tag bordered={false} color={a.slot === 'home' ? 'blue' : 'purple'}>{a.slot === 'home' ? '首页横幅' : '开屏弹窗'}</Tag> },
          { title: '图片', width: 130, render: (_, a) => (a.image ? <img src={preview(a.image)} alt="" style={{ width: 110, height: 44, objectFit: 'cover', borderRadius: 6 }} /> : <span className="muted">纯文字</span>) },
          { title: '标题', render: (_, a) => <>{a.title || <span className="muted">—</span>}{a.link && <div className="muted" style={{ fontSize: 12 }}>{a.link}</div>}</> },
          { title: '投放时间', width: 190, render: (_, a) => <span className="muted">{a.start_at ? dayjs(a.start_at).format('MM-DD HH:mm') : '立即'} ~ {a.end_at ? dayjs(a.end_at).format('MM-DD HH:mm') : '长期'}</span> },
          { title: '排序', dataIndex: 'sort', width: 60 },
          {
            title: '启用',
            width: 70,
            render: (_, a) => <Switch size="small" checked={a.enabled} onChange={(v) => { const next = items.map((x) => (x.id === a.id ? { ...x, enabled: v } : x)); setItems(next); save(next) }} />,
          },
          {
            title: '',
            width: 120,
            render: (_, a) => (
              <Space size={0}>
                <Button type="link" size="small" onClick={() => openAd(a)}>编辑</Button>
                <Popconfirm title="删除这条广告？" onConfirm={() => { const next = items.filter((x) => x.id !== a.id); setItems(next); save(next) }}>
                  <Button type="link" size="small" danger>删除</Button>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
      <Drawer open={!!edit} onClose={() => setEdit(undefined)} size={520} title={edit && items.some((x) => x.id === edit.id) ? '编辑广告' : '添加广告'}
        extra={<Space><Button onClick={() => setEdit(undefined)}>取消</Button><Button type="primary" loading={busy} onClick={saveAd}>保存</Button></Space>}>
        <Form form={adForm} layout="vertical">
          <Form.Item name="id" hidden><Input /></Form.Item>
          <Form.Item name="slot" label="位置">
            <Segmented options={[{ label: '首页横幅', value: 'home' }, { label: '开屏弹窗 / 通知', value: 'popup' }]} />
          </Form.Item>
          <Form.Item name="title" label="标题" rules={[{ required: adSlot === 'popup', message: '弹窗需要标题' }]}><Input maxLength={40} /></Form.Item>
          {adSlot === 'popup' && <Form.Item name="text" label="正文" extra="没有图片时作为通知内容显示"><Input.TextArea rows={3} maxLength={300} /></Form.Item>}
          <Form.Item label={adSlot === 'home' ? '图片（建议 1080×360，2 MB 以内）' : '图片（可选，建议 900×1200）'} required={adSlot === 'home'}>
            <Space direction="vertical" style={{ width: '100%' }}>
              <Upload accept="image/png,image/jpeg,image/webp,image/gif" showUploadList={false} beforeUpload={uploadImage}>
                <Button icon={<PictureOutlined />}>上传图片</Button>
              </Upload>
              <Form.Item name="image" noStyle rules={[{ required: adSlot === 'home', message: '请上传图片或填写图片地址' }]}>
                <Input placeholder="或填写图片地址 https://…" />
              </Form.Item>
              {adImage && <img src={adImage} alt="" style={{ maxWidth: '100%', maxHeight: 180, borderRadius: 8 }} />}
            </Space>
          </Form.Item>
          <Form.Item name="link" label="点击跳转链接" extra="留空则不可点击；用系统浏览器打开" rules={[{ pattern: /^https?:\/\//, message: '以 http(s):// 开头' }]}>
            <Input placeholder="https://…" />
          </Form.Item>
          <Space size={16} wrap>
            <Form.Item name="start_at" label="开始时间" {...toISO}><DatePicker showTime={{ format: 'HH:mm' }} format="YYYY-MM-DD HH:mm" /></Form.Item>
            <Form.Item name="end_at" label="结束时间" {...toISO}><DatePicker showTime={{ format: 'HH:mm' }} format="YYYY-MM-DD HH:mm" /></Form.Item>
          </Space>
          <Space size={24}>
            <Form.Item name="sort" label="排序（小的在前）"><InputNumber /></Form.Item>
            <Form.Item name="enabled" label="启用" valuePropName="checked"><Switch /></Form.Item>
          </Space>
        </Form>
      </Drawer>
    </>
  )
}

// ---- guard ----

function GuardTab({ value, onSaved }: { value: GuardSettings; onSaved: (g: GuardSettings) => void }) {
  const [form] = Form.useForm<GuardSettings>()
  const { busy, run } = useAction()
  useEffect(() => form.setFieldsValue(value), [value, form])
  const save = async () => {
    const v = await form.validateFields()
    let saved: GuardSettings | undefined
    if (await run(async () => (saved = await api.settings.saveSection('guard', { ...value, ...v })), '防护设置已保存')) onSaved(saved!)
  }
  return (
    <Form form={form} labelCol={{ flex: '170px' }} wrapperCol={{ flex: '1' }} style={narrow}>
      <Alert type="info" showIcon icon={<SafetyOutlined />} style={{ marginBottom: 16 }}
        message="所有接口按 IP 限流；频率异常、认证反复失败、扫描漏洞路径的 IP 会被自动封禁一段时间。被封列表和手动解封在「风控 → 自动封禁」。" />
      <Form.Item name="disabled" label="启用自动防护" getValueFromEvent={(v: boolean) => !v} getValueProps={(v: boolean) => ({ checked: !v })}><Switch /></Form.Item>
      <Form.Item name="rate_per_minute" label="单 IP 每分钟请求上限" extra="超过返回「请求过于频繁」；超过 3 倍直接封禁。App 正常使用每分钟不到 20 次">
        <NumberUnit min={30} max={100000} unit="次" />
      </Form.Item>
      <Form.Item name="errors_per_10m" label="10 分钟异常请求上限" extra="认证失败、请求不存在的接口、格式错误等累计到这个数就封禁（认证失败记 3 次）">
        <NumberUnit min={10} max={100000} unit="次" />
      </Form.Item>
      <Form.Item name="ban_minutes" label="自动封禁时长"><NumberUnit min={1} max={10080} unit="分钟" /></Form.Item>
      <Form.Item name="allow_ips" label="白名单 IP" extra="公司网络、支付中心、监控服务等，不受限流和封禁">
        <Select mode="tags" tokenSeparators={[',', ' ']} placeholder="IP 或网段，回车添加" />
      </Form.Item>
      <Form.Item label=" " colon={false}><SaveBar busy={busy} onSave={save} /></Form.Item>
    </Form>
  )
}

function SystemInfoView({ sys }: { sys: SystemInfo }) {
  return (
    <div style={narrow}>
      {sys.mock_payments && <Alert type="error" showIcon style={{ marginBottom: 12 }} message="模拟支付已开启：任何人都能免费开通套餐，上线前请在 api.json 关闭 payments.mock" />}
      <Descriptions column={1} size="small" bordered>
        <Descriptions.Item label="对外地址">{sys.public_url || '—'}</Descriptions.Item>
        <Descriptions.Item label="开放邮箱注册">{sys.allow_register ? '是' : '否'}</Descriptions.Item>
        <Descriptions.Item label="api.json 支付方式">{sys.payment_methods.join('、') || '未配置'}</Descriptions.Item>
        <Descriptions.Item label="模拟支付">{sys.mock_payments ? <Tag color="error">已开启</Tag> : '关闭'}</Descriptions.Item>
      </Descriptions>
      <p className="muted" style={{ marginTop: 12 }}>以上来自服务器上的 api.json，修改后需重启服务。<ReloadOutlined /></p>
    </div>
  )
}
