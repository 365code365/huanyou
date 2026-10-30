'use client'
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import { AutoComplete, Avatar, Button, Drawer, Form, Input, InputNumber, Space, Switch, Table } from 'antd'
import { PageCard } from '@/components/PageCard'
import { useCatalog } from '@/hooks/useCatalog'
import { api, type AppRec } from '@/lib/api'

const blank = (): AppRec => ({ id: '', name: '', icon_url: '', url: '', category: 'AI', desc: '', disabled: false, sort: 0 })

export default function AppsPage() {
  const c = useCatalog(api.apps, blank, '应用')
  const categories = [...new Set(['AI', '社交', '视频', '工具', ...c.items.map((a) => a.category).filter(Boolean)])]

  return (
    <div className="page">
      <PageCard>
        <div className="toolbar">
          <span className="muted">App 内「应用」页按分类展示，排序值小的在前</span>
          <div className="spacer" />
          <Button icon={<ReloadOutlined />} onClick={() => c.load()}>刷新</Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => c.openCreate({ sort: (c.items.at(-1)?.sort ?? 0) + 10 })}>新增应用</Button>
        </div>
        <Table<AppRec>
          rowKey="id"
          loading={c.loading}
          dataSource={c.items}
          pagination={false}
          columns={[
            {
              title: '应用',
              render: (_, a) => (
                <Space>
                  <Avatar shape="square" size={36} src={a.icon_url || undefined}>{a.name.slice(0, 1)}</Avatar>
                  <div>
                    <div>{a.name} <span className="muted">{a.id}</span></div>
                    <div className="muted">{a.desc}</div>
                  </div>
                </Space>
              ),
            },
            { title: '分类', dataIndex: 'category', width: 110 },
            { title: '链接', render: (_, a) => <a href={a.url} target="_blank" rel="noreferrer">{a.url}</a> },
            { title: '排序', dataIndex: 'sort', width: 70 },
            { title: '上架', width: 80, render: (_, a) => <Switch checked={!a.disabled} onChange={(v) => c.patch(a, { disabled: !v })} /> },
            {
              title: '操作',
              width: 120,
              render: (_, a) => (
                <Space size={0}>
                  <Button type="link" size="small" onClick={() => c.openEdit(a)}>编辑</Button>
                  <Button type="link" size="small" danger onClick={() => c.remove(a)}>删除</Button>
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
        title={c.mode === 'create' ? '新增应用' : '编辑应用'}
        extra={<Space><Button onClick={c.close}>取消</Button><Button type="primary" loading={c.saving} onClick={() => c.save()}>保存</Button></Space>}
      >
        <Form form={c.form} layout="vertical">
          <Form.Item name="id" label="ID" rules={[{ required: true, pattern: /^[a-z0-9][a-z0-9_-]{0,31}$/, message: '小写字母 / 数字 / - / _' }]}>
            <Input disabled={c.mode === 'edit'} placeholder="如 chatgpt" />
          </Form.Item>
          <Form.Item name="name" label="名称" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="icon_url" label="图标 URL" extra="建议 128×128 PNG；留空显示名称首字">
            <Input placeholder="https://..." />
          </Form.Item>
          <Form.Item name="url" label="跳转链接" rules={[{ required: true, type: 'url', message: '请输入有效链接' }]}>
            <Input placeholder="App Store / 官网 / 下载地址" />
          </Form.Item>
          <Form.Item name="category" label="分类">
            <AutoComplete options={categories.map((v) => ({ value: v }))} />
          </Form.Item>
          <Form.Item name="desc" label="简介"><Input /></Form.Item>
          <Form.Item name="sort" label="排序"><InputNumber /></Form.Item>
          <Form.Item name="disabled" label="上架" getValueFromEvent={(v: boolean) => !v} getValueProps={(v: boolean) => ({ checked: !v })}>
            <Switch />
          </Form.Item>
        </Form>
      </Drawer>
    </div>
  )
}
