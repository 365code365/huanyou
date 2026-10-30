'use client'
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import { Button, Drawer, Form, Input, InputNumber, Segmented, Select, Space, Switch, Table, Tag } from 'antd'
import { PageCard } from '@/components/PageCard'
import { useCatalog } from '@/hooks/useCatalog'
import { api, type Contact } from '@/lib/api'

/** Contact types (must match config.ContactTypes on the server). */
const TYPES: { value: string; label: string; color: string; hint: string; needsUrl?: boolean }[] = [
  { value: 'telegram', label: 'Telegram 飞机', color: '#229ED9', hint: '用户名或频道名，如 orbit_support（不带 @）' },
  { value: 'x', label: 'X (Twitter)', color: '#111', hint: '账号名，如 orbitvpn' },
  { value: 'instagram', label: 'Instagram', color: '#E1306C', hint: '账号名，如 orbit.vpn' },
  { value: 'facebook', label: 'Facebook', color: '#1877F2', hint: '主页名' },
  { value: 'youtube', label: 'YouTube', color: '#FF0000', hint: '频道 @ 名' },
  { value: 'tiktok', label: 'TikTok', color: '#111', hint: '账号名' },
  { value: 'discord', label: 'Discord', color: '#5865F2', hint: '邀请链接填在「链接」', needsUrl: true },
  { value: 'whatsapp', label: 'WhatsApp', color: '#25D366', hint: '带国家码的手机号，如 +1 555 123 4567' },
  { value: 'wechat', label: '微信', color: '#07C160', hint: '微信号（App 内点击复制）' },
  { value: 'qq', label: 'QQ', color: '#12B7F5', hint: 'QQ 号或群号（App 内点击复制）' },
  { value: 'email', label: '邮箱', color: '#555', hint: '邮箱地址' },
  { value: 'website', label: '网站', color: '#555', hint: '网址', needsUrl: false },
]
const typeOf = (v: string) => TYPES.find((t) => t.value === v)

const blank = (): Contact => ({
  id: '', type: 'telegram', group: 'service', name: '', value: '', url: '', desc: '', disabled: false, sort: 0,
})
// "link" is computed by the server; never send it back.
const strip = ({ link: _l, ...k }: Contact) => k as Contact

export default function ContactsPage() {
  const c = useCatalog(api.contacts, blank, '联系方式')
  const type = Form.useWatch('type', c.form) as string | undefined
  const t = typeOf(type ?? '')

  return (
    <div className="page">
      <PageCard>
        <div className="toolbar">
          <span className="muted">显示在 App「我的」页：「联系客服」列表和「关注我们」图标，排序值小的在前</span>
          <div className="spacer" />
          <Button icon={<ReloadOutlined />} onClick={() => c.load()}>刷新</Button>
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => c.openCreate({ sort: (c.items.at(-1)?.sort ?? 0) + 10 })}
          >
            新增
          </Button>
        </div>
        <Table<Contact>
          rowKey="id"
          loading={c.loading}
          dataSource={c.items}
          pagination={false}
          locale={{ emptyText: '还没有配置联系方式，App 中不会显示这一栏' }}
          columns={[
            {
              title: '名称',
              render: (_, k) => (
                <>
                  <div>
                    <Tag color={typeOf(k.type)?.color} bordered={false} style={{ color: '#fff' }}>{typeOf(k.type)?.label ?? k.type}</Tag>
                    {k.name}
                  </div>
                  {k.desc && <div className="muted">{k.desc}</div>}
                </>
              ),
            },
            { title: '分组', width: 100, render: (_, k) => (k.group === 'social' ? '关注我们' : '联系客服') },
            { title: '账号', dataIndex: 'value', render: (v) => <span className="mono">{v}</span> },
            {
              title: 'App 点击后',
              render: (_, k) =>
                k.link ? <a href={k.link} target="_blank" rel="noreferrer">{k.link}</a> : <span className="muted">复制账号</span>,
            },
            { title: '排序', dataIndex: 'sort', width: 70 },
            { title: '显示', width: 80, render: (_, k) => <Switch checked={!k.disabled} onChange={(v) => c.patch(k, { disabled: !v }, strip)} /> },
            {
              title: '操作',
              width: 120,
              render: (_, k) => (
                <Space size={0}>
                  <Button type="link" size="small" onClick={() => c.openEdit(k)}>编辑</Button>
                  <Button type="link" size="small" danger onClick={() => c.remove(k)}>删除</Button>
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
        title={c.mode === 'create' ? '新增联系方式' : '编辑联系方式'}
        extra={<Space><Button onClick={c.close}>取消</Button><Button type="primary" loading={c.saving} onClick={() => c.save(strip)}>保存</Button></Space>}
      >
        <Form form={c.form} layout="vertical">
          <Form.Item name="group" label="显示在">
            <Segmented options={[{ label: '联系客服', value: 'service' }, { label: '关注我们', value: 'social' }]} />
          </Form.Item>
          <Form.Item name="type" label="类型" rules={[{ required: true }]}>
            <Select options={TYPES.map(({ value, label }) => ({ value, label }))} />
          </Form.Item>
          <Form.Item name="id" label="ID" rules={[{ required: true, pattern: /^[a-z0-9][a-z0-9_-]{0,31}$/, message: '小写字母 / 数字 / - / _' }]}>
            <Input disabled={c.mode === 'edit'} placeholder="如 tg-support" />
          </Form.Item>
          <Form.Item name="name" label="显示名称" extra="留空使用类型名">
            <Input placeholder={t ? t.label.split(' ')[0] + ' 客服' : ''} />
          </Form.Item>
          <Form.Item
            name="value"
            label="账号"
            extra={t?.hint}
            rules={[{ required: !t?.needsUrl, message: '请填写账号' }]}
          >
            <Input />
          </Form.Item>
          <Form.Item
            name="url"
            label="链接（可选）"
            extra="一般不用填，会按类型自动生成，例如 Telegram → https://t.me/账号"
            rules={[{ required: !!t?.needsUrl, message: '请填写链接' }]}
          >
            <Input placeholder="https://..." />
          </Form.Item>
          <Form.Item name="desc" label="说明" extra="如「7×24 小时在线」「最新公告与优惠」">
            <Input maxLength={40} />
          </Form.Item>
          <Form.Item name="sort" label="排序"><InputNumber /></Form.Item>
          <Form.Item name="disabled" label="显示" getValueFromEvent={(v: boolean) => !v} getValueProps={(v: boolean) => ({ checked: !v })}>
            <Switch />
          </Form.Item>
        </Form>
      </Drawer>
    </div>
  )
}
