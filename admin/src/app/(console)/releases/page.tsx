'use client'
import { AndroidFilled, AppleFilled, CloudUploadOutlined, InboxOutlined, LinkOutlined, ReloadOutlined } from '@ant-design/icons'
import { Alert, App, Button, Form, Input, InputNumber, Modal, Progress, Segmented, Space, Switch, Table, Tag, Upload } from 'antd'
import type { UploadFile } from 'antd'
import { useMemo, useState } from 'react'
import { PageCard } from '@/components/PageCard'
import { useAction, useAsync } from '@/hooks/useAsync'
import { api, type Release } from '@/lib/api'
import { bytes, date, errMsg } from '@/lib/format'

type Platform = 'android' | 'ios'

/** Next version suggestion: 1.2.3 -> 1.2.4. */
const bump = (v?: string) => {
  if (!v) return '1.0.0'
  const p = v.split('.').map((x) => parseInt(x, 10) || 0)
  p[p.length - 1]++
  return p.join('.')
}

export default function ReleasesPage() {
  const { message, modal } = App.useApp()
  const { data, loading, reload, setData } = useAsync(api.releases.list)
  const items = useMemo(() => data?.items ?? [], [data])
  const { run } = useAction()

  const [open, setOpen] = useState(false)
  const [platform, setPlatform] = useState<Platform>('android')
  const [source, setSource] = useState<'file' | 'url'>('file')
  const [file, setFile] = useState<UploadFile[]>([])
  const [progress, setProgress] = useState<number>()
  const [form] = Form.useForm()

  const latest = (p: Platform) => items.find((r) => r.platform === p) // list is newest build first per platform

  const openCreate = (p: Platform) => {
    const l = latest(p)
    setPlatform(p)
    setSource(p === 'android' ? 'file' : 'url')
    setFile([])
    setProgress(undefined)
    form.resetFields()
    form.setFieldsValue({
      version: bump(l?.version),
      build: (l?.build ?? 0) + 1,
      notes: '',
      force: false,
      bundle_id: p === 'ios' ? (l?.bundle_id || 'app.cygnus6152.artichoke66') : '',
    })
    setOpen(true)
  }

  const submit = async () => {
    const v = await form.validateFields()
    if (source === 'file' && !file[0]?.originFileObj) return message.warning('请选择安装包')
    const fd = new FormData()
    // Text fields first: the server streams the file and needs the platform before it.
    fd.append('platform', platform)
    fd.append('version', v.version.trim())
    fd.append('build', String(v.build))
    fd.append('notes', v.notes ?? '')
    fd.append('force', String(!!v.force))
    if (platform === 'ios') fd.append('bundle_id', v.bundle_id ?? '')
    if (source === 'url') fd.append('url', v.url.trim())
    else fd.append('file', file[0].originFileObj as Blob, file[0].name)
    setProgress(0)
    try {
      await api.releases.create(fd, setProgress)
      message.success('已发布，App 内检查更新即可看到')
      setOpen(false)
      reload()
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setProgress(undefined)
    }
  }

  const patch = async (r: Release, change: Partial<Release>) => {
    if (await run(() => api.releases.patch(r.id, change), '已更新')) {
      setData((d) => d && { ...d, items: d.items.map((x) => (x.id === r.id ? { ...x, ...change } : x)) })
    }
  }

  const remove = (r: Release) =>
    modal.confirm({
      title: '删除版本',
      content: `删除 ${r.platform === 'ios' ? 'iOS' : 'Android'} ${r.version} (${r.build})？安装包文件也会删除。`,
      okButtonProps: { danger: true },
      onOk: async () => {
        if (await run(() => api.releases.remove(r.id), '已删除')) reload()
      },
    })

  const uploading = progress !== undefined
  const fileName = file[0]?.name
  const wantExt = platform === 'android' ? '.apk' : '.ipa'

  return (
    <div className="page">
      <PageCard>
        <div className="toolbar">
          <span className="muted">App 启动和「我的 → 检查更新」读取每个平台最新的已发布版本；标记「强制更新」后，旧版本必须升级才能继续使用</span>
          <div className="spacer" />
          <Button icon={<ReloadOutlined />} onClick={() => reload()}>刷新</Button>
          <Button icon={<AppleFilled />} onClick={() => openCreate('ios')}>发布 iOS 版本</Button>
          <Button type="primary" icon={<AndroidFilled />} onClick={() => openCreate('android')}>发布 Android 版本</Button>
        </div>
        <Table<Release>
          rowKey="id"
          loading={loading}
          dataSource={items}
          pagination={false}
          locale={{ emptyText: '还没有发布版本' }}
          columns={[
            {
              title: '版本',
              render: (_, r) => (
                <Space>
                  {r.platform === 'ios' ? <AppleFilled style={{ fontSize: 18 }} /> : <AndroidFilled style={{ fontSize: 18, color: '#3ddc84' }} />}
                  <div>
                    <div style={{ fontWeight: 600 }}>
                      {r.version} <span className="muted">build {r.build}</span>
                      {latest(r.platform)?.id === r.id && r.published && <Tag color="success" bordered={false} style={{ marginLeft: 6 }}>最新</Tag>}
                    </div>
                    <div className="muted" style={{ maxWidth: 360, whiteSpace: 'pre-line' }}>{r.notes || '—'}</div>
                  </div>
                </Space>
              ),
            },
            {
              title: '安装包',
              width: 220,
              render: (_, r) =>
                r.file ? (
                  <>
                    <div>{bytes(r.size)}</div>
                    <div className="muted mono" title={r.sha256}>sha256 {r.sha256.slice(0, 12)}…</div>
                  </>
                ) : (
                  <a href={r.url} target="_blank" rel="noreferrer"><LinkOutlined /> 外部链接</a>
                ),
            },
            { title: '发布时间', width: 120, render: (_, r) => date(r.created_at) },
            { title: '强制更新', width: 90, render: (_, r) => <Switch size="small" checked={r.force} onChange={(v) => patch(r, { force: v })} /> },
            { title: '已发布', width: 80, render: (_, r) => <Switch size="small" checked={r.published} onChange={(v) => patch(r, { published: v })} /> },
            {
              title: '操作',
              width: 130,
              render: (_, r) => (
                <Space size={0}>
                  <Button type="link" size="small" href={r.download_url} target="_blank">下载</Button>
                  <Button type="link" size="small" danger onClick={() => remove(r)}>删除</Button>
                </Space>
              ),
            },
          ]}
        />
      </PageCard>

      <Modal
        open={open}
        title={platform === 'ios' ? '发布 iOS 版本' : '发布 Android 版本'}
        onCancel={() => !uploading && setOpen(false)}
        maskClosable={!uploading}
        closable={!uploading}
        width={560}
        okText={uploading ? '上传中…' : '发布'}
        okButtonProps={{ icon: <CloudUploadOutlined />, loading: uploading }}
        cancelButtonProps={{ disabled: uploading }}
        onOk={submit}
        destroyOnHidden
      >
        <Form form={form} layout="vertical" requiredMark={false}>
          <Space size={12} style={{ display: 'flex' }}>
            <Form.Item name="version" label="版本号" rules={[{ required: true, pattern: /^\d{1,4}(\.\d{1,4}){0,3}$/, message: '如 1.2.0' }]}>
              <Input style={{ width: 150 }} placeholder="1.2.0" />
            </Form.Item>
            <Form.Item
              name="build"
              label="构建号"
              tooltip="与 pubspec.yaml 里 version 的 + 后面一致（1.2.0+5 的 5），必须比上一版大"
              rules={[{ required: true, message: '必填' }]}
            >
              <InputNumber min={1} style={{ width: 130 }} />
            </Form.Item>
          </Space>

          <Form.Item label="安装包来源">
            <Segmented
              value={source}
              onChange={(v) => setSource(v as 'file' | 'url')}
              options={[
                { label: platform === 'ios' ? '上传 IPA' : '上传 APK', value: 'file' },
                { label: platform === 'ios' ? 'TestFlight / App Store 链接' : '外部链接', value: 'url' },
              ]}
            />
          </Form.Item>

          {source === 'file' ? (
            <>
              <Form.Item>
                <Upload.Dragger
                  accept={wantExt}
                  maxCount={1}
                  fileList={file}
                  beforeUpload={(f) => {
                    if (!f.name.toLowerCase().endsWith(wantExt)) {
                      message.error(`请选择 ${wantExt} 文件`)
                      return Upload.LIST_IGNORE
                    }
                    return false // upload on submit, with the form fields
                  }}
                  onChange={({ fileList }) => setFile(fileList.slice(-1))}
                  disabled={uploading}
                >
                  <p className="ant-upload-drag-icon"><InboxOutlined /></p>
                  <p className="ant-upload-text">{fileName ?? `点击或拖拽 ${wantExt} 到这里`}</p>
                  <p className="ant-upload-hint">最大 600 MB，服务器会计算 SHA-256 供 App 校验</p>
                </Upload.Dragger>
              </Form.Item>
              {platform === 'ios' && (
                <>
                  <Form.Item name="bundle_id" label="Bundle ID" rules={[{ required: true, message: '必填' }]}>
                    <Input className="mono" />
                  </Form.Item>
                  <Alert
                    type="info"
                    showIcon
                    style={{ marginBottom: 16 }}
                    message="IPA 需为 Ad-hoc / 企业签名。下载页会生成 itms-services 安装链接，测试设备用 Safari 打开即可安装（设备需在描述文件里）。"
                  />
                </>
              )}
            </>
          ) : (
            <Form.Item
              name="url"
              label="下载链接"
              rules={[{ required: true, pattern: /^(https:\/\/|itms-services:\/\/)/, message: '必须是 https:// 链接' }]}
            >
              <Input placeholder={platform === 'ios' ? 'https://testflight.apple.com/join/xxxx' : 'https://...'} />
            </Form.Item>
          )}

          <Form.Item name="notes" label="更新说明" extra="每行一条，显示在 App 更新弹窗和下载页">
            <Input.TextArea rows={4} placeholder={'修复断网后不能自动重连\n新增检查更新'} />
          </Form.Item>
          <Form.Item name="force" label="强制更新" valuePropName="checked" extra="开启后低于此版本的用户必须更新才能继续使用">
            <Switch />
          </Form.Item>
          {uploading && <Progress percent={Math.round((progress ?? 0) * 100)} status="active" />}
        </Form>
      </Modal>
    </div>
  )
}
