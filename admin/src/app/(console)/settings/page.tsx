'use client'
import { CopyOutlined, FileTextOutlined, KeyOutlined, LinkOutlined, PlusOutlined, ReloadOutlined, SafetyOutlined, SearchOutlined } from '@ant-design/icons'
import {
  Alert, App, Button, DatePicker, Descriptions, Drawer, Form, Input, InputNumber, Modal, Popconfirm, Segmented, Select, Space, Spin, Switch, Table, Tabs, Tag, Typography,
} from 'antd'
import dayjs from 'dayjs'
import { ImageUpload } from '@/components/ImageUpload'
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

const httpsOrLocal = /^(https:\/\/|http:\/\/(127\.0\.0\.1|localhost)[:/])/

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
  const [queryOpen, setQueryOpen] = useState(false)
  const [queryId, setQueryId] = useState('')
  const [querying, setQuerying] = useState(false)
  const [queryResult, setQueryResult] = useState<Awaited<ReturnType<typeof api.payment.testQuery>>>()
  const testQuery = async () => {
    setQuerying(true)
    try {
      setQueryResult(await api.payment.testQuery(queryId.trim()))
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setQuerying(false)
    }
  }
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
        <h4 className="form-section">App Store 审核（iOS 支付 A/B 面）</h4>
        <Alert type="warning" showIcon style={{ marginBottom: 16 }}
          message="苹果要求数字商品走 App 内购买（IAP）。提审前把下面填成提交审核的 iOS 构建号，审核版本就只显示 Apple 内购、且不出现网页支付和第三方应用推荐；审核通过后改回 0，这些 iOS 用户立即切回下方的支付中心。只影响 iOS，安卓/桌面不受影响。" />
        <Form.Item name="ios_review_build" label="审核中的 iOS 构建号"
          extra="0 = 关闭（所有 iOS 用户用网页支付）。填写后，构建号 ≥ 此值的 iOS 客户端进入「审核面」（Apple 内购）。请填「版本发布」里提交审核的那个 iOS 构建号。">
          <InputNumber min={0} max={1000000} style={{ width: 200 }} placeholder="0" />
        </Form.Item>
        <h4 className="form-section">Google Play 版（安卓支付面）</h4>
        <Alert type="info" showIcon style={{ marginBottom: 16 }}
          message="Google Play 版是单独的构建渠道（编译时 --dart-define=CHANNEL=play），固定走 Google Play 结算，不显示网页支付、第三方应用推荐和应用内更新，连接前有 VpnService 用途说明——对所有用户一致，不受此处开关影响，也无需在审核后切换。每个付费套餐在「套餐」页填写与 Play Console「一次性商品」一致的 Google Play 产品 ID；凭证校验的服务账号在 api.json 的 payments.google_play 配置。官网 APK（site 渠道）仍用下方支付中心。" />
        <h4 className="form-section">用户协议 / 隐私政策</h4>
        <Alert type="info" showIcon style={{ marginBottom: 16 }}
          message="App 购买页底部会显示《用户协议》《隐私政策》链接（苹果审核要求）。留空则使用系统内置页面：{对外地址}/legal/terms 和 /legal/privacy；App Store Connect 的「隐私政策网址」也可填内置地址。" />
        <Form.Item name="terms_url" label="用户协议链接" rules={[{ pattern: /^https?:\/\//, message: '必须是 http(s) 地址' }]}>
          <Input allowClear placeholder="留空 = 内置 /legal/terms" />
        </Form.Item>
        <Form.Item name="privacy_url" label="隐私政策链接" rules={[{ pattern: /^https?:\/\//, message: '必须是 http(s) 地址' }]}>
          <Input allowClear placeholder="留空 = 内置 /legal/privacy" />
        </Form.Item>
        <h4 className="form-section">支付中心（下单链接）</h4>
        <Form.Item name={['gateway', 'enabled']} label="启用" valuePropName="checked"><Switch /></Form.Item>
        <Form.Item name={['gateway', 'name']} label="App 显示名称"><Input maxLength={20} placeholder="在线支付" /></Form.Item>
        <Alert type="info" showIcon style={{ marginBottom: 16 }}
          message="对接外部支付中心：支付中心提供「下单链接」和「订单查询接口」，本系统提供「支付结果通知」接口。付款后支付中心立即通知本系统；通知丢失时本系统会主动调用查询接口确认，两条路径都能开通套餐。" />
        <Form.Item name={['gateway', 'pay_link']} label="下单链接" rules={[{ required: gwOn, message: '请填写支付中心的收银台地址' }, { pattern: httpsOrLocal, message: '必须是 https 地址' }]}
          extra="支付中心的收银台。用户下单后打开：下单链接?out_trade_no=订单号&amount=金额（分）&…&sign=签名">
          <Input placeholder="https://pay.example.com/checkout" />
        </Form.Item>
        <Form.Item name={['gateway', 'query_url']} label="订单查询接口" rules={[{ pattern: httpsOrLocal, message: '必须是 https 地址' }]}
          extra="支付中心提供，本系统主动调用它确认订单是否已支付（通知丢失或延迟时兜底）。强烈建议填写；留空则只能依靠通知">
          <Input placeholder="https://pay.example.com/api/order/query" />
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
        <Form.Item label="本系统通知接口" extra="固定地址，交给支付中心：付款成功后支付中心 POST 到这里">
          <Typography.Text className="mono" copyable>{ep.notify}</Typography.Text>
        </Form.Item>
        <Form.Item name={['gateway', 'allow_ips']} label="通知 IP 白名单" extra="可选：只接受支付中心服务器发来的通知（IP 或网段），留空不限制">
          <Select mode="tags" tokenSeparators={[',', ' ']} placeholder="如 203.0.113.10" />
        </Form.Item>
        <Form.Item label=" " colon={false}>
          <Space wrap>
            <SaveBar busy={busy} onSave={save} />
            <Button icon={<FileTextOutlined />} onClick={async () => setDoc((await api.payment.doc()).markdown)}>查看对接文档</Button>
            <Button icon={<LinkOutlined />} onClick={testLink}>生成测试链接</Button>
            <Button icon={<SearchOutlined />} onClick={() => { setQueryResult(undefined); setQueryOpen(true) }}>测试查询</Button>
          </Space>
        </Form.Item>
      </Form>
      {loaded.system.payment_methods.length > 0 && (
        <Alert type="info" showIcon style={{ ...narrow, marginTop: 8 }} message={`api.json 里还配置了：${loaded.system.payment_methods.join('、')}（同样受「开放购买」开关控制）`} />
      )}
      <Modal open={queryOpen} title="测试订单查询接口" onCancel={() => setQueryOpen(false)} footer={null} destroyOnHidden>
        <p className="muted">用已保存的设置调用支付中心的订单查询接口。可以填刚才「生成测试链接」的订单号，或真实订单号；留空用一个随机订单号（应返回「没有这个订单」）。</p>
        <Space.Compact style={{ width: '100%' }}>
          <Input placeholder="订单号，如 VP2026…" value={queryId} onChange={(e) => setQueryId(e.target.value)} onPressEnter={testQuery} />
          <Button type="primary" loading={querying} onClick={testQuery}>查询</Button>
        </Space.Compact>
        {queryResult && (
          <Alert style={{ marginTop: 14 }} showIcon
            type={!queryResult.ok ? 'error' : queryResult.found ? 'success' : 'info'}
            message={!queryResult.ok ? `查询失败（${queryResult.ms} ms）` : queryResult.found ? `查到订单（${queryResult.ms} ms）` : `接口正常（${queryResult.ms} ms）`}
            description={queryResult.order
              ? <pre className="mono" style={{ margin: 0, whiteSpace: 'pre-wrap' }}>{JSON.stringify(queryResult.order, null, 2)}</pre>
              : queryResult.message} />
        )}
      </Modal>
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
  const [popupAspect, setPopupAspect] = useState<number>(3 / 4)
  const uploadImage = async (file: File) => {
    const fd = new FormData()
    fd.append('file', file)
    return (await api.ads.upload(fd)).url
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
          <Form.Item label={adSlot === 'home' ? '图片（按 3:1 裁剪，App 首页横幅比例）' : '图片（可选）'} required={adSlot === 'home'}
            extra="上传后可在线裁剪、缩放、旋转，并自动压缩（GIF 不裁剪，需小于 2 MB）">
            <Space direction="vertical" style={{ width: '100%' }}>
              <Space wrap>
                {adSlot === 'popup' && (
                  <Segmented size="small" value={popupAspect} onChange={(v) => setPopupAspect(v as number)}
                    options={[{ label: '竖图 3:4', value: 3 / 4 }, { label: '方图 1:1', value: 1 }, { label: '横图 4:3', value: 4 / 3 }, { label: '不裁剪', value: 0 }]} />
                )}
                <ImageUpload
                  key={adSlot + popupAspect}
                  aspect={adSlot === 'home' ? 3 : popupAspect || undefined}
                  maxWidth={adSlot === 'home' ? 1080 : 900}
                  upload={uploadImage}
                  onUploaded={(url) => adForm.setFieldValue('image', url)}
                />
              </Space>
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
      <Form.Item name="new_devices_per_ip_day" label="单 IP 每天新开设备上限"
        extra="同一 IP（IPv6 按 /64）一天最多新开通的设备账号数，防止脚本批量刷免费体验和邀请奖励。其中最多 3 个能记邀请奖励。学校、公司或运营商共享出口 IP 时可适当调大">
        <NumberUnit min={1} max={100000} unit="个" />
      </Form.Item>
      <Form.Item name="allow_ips" label="白名单 IP" extra="公司网络、支付中心、监控服务等，不受限流和封禁">
        <Select mode="tags" tokenSeparators={[',', ' ']} placeholder="IP 或网段，回车添加" />
      </Form.Item>
        <h4 className="form-section">App 请求安全（防抓包 / 防篡改）</h4>
        <Alert type="info" showIcon style={{ marginBottom: 16 }}
          message="1.2.5 起，App 与服务器用登录时协商的会话密钥给每个请求签名（防伪造、防重放），返回内容全部加密，并且只信任官方证书（装了抓包证书也看不到内容）。下面三项决定对旧版本和可疑设备的处理。" />
        <Form.Item name="require_signed" label="拒绝未签名的旧版本请求" valuePropName="checked"
          extra="开启后 1.2.4 及更早版本无法登录和使用，会提示更新。建议等「数据分析」里旧版本用户基本升级后再开。">
          <Switch />
        </Form.Item>
        <Form.Item name="app_signers" label="官方安卓签名证书（SHA-256）"
          extra="填写后，被重新打包（签名不同）的安卓 App 一律拒绝服务。打包脚本 scripts/build_android.sh 会打印这个值；留空不检查。">
          <Select mode="tags" tokenSeparators={[',', ' ', '\n']} placeholder="631b7059…（64 位十六进制）" open={false} />
        </Form.Item>
        <Form.Item name="block_tampered" label="拒绝检测到调试 / Hook 工具的设备" valuePropName="checked"
          extra="App 会上报是否检测到调试器、Frida、Xposed 等注入工具。关闭时只记日志；开启后这些设备无法使用。Root / 越狱本身不拦截。">
          <Switch />
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
