'use client'
import { AndroidFilled, AppleFilled, AppleOutlined, CloudUploadOutlined, InboxOutlined, LinkOutlined, ReloadOutlined, WindowsFilled } from '@ant-design/icons'
import { Alert, App, Button, Form, Input, InputNumber, Modal, Progress, Segmented, Select, Space, Switch, Table, Tag, Upload } from 'antd'
import type { UploadFile } from 'antd'
import { useMemo, useState } from 'react'
import { PageCard } from '@/components/PageCard'
import { useAction, useAsync } from '@/hooks/useAsync'
import { api, type Release } from '@/lib/api'
import { bytes, date, errMsg } from '@/lib/format'

type Platform = 'android' | 'ios' | 'windows' | 'macos'

const platformLabel: Record<Platform, string> = { android: 'Android', ios: 'iOS', windows: 'Windows', macos: 'macOS' }
/** Package each platform uploads; desktop zips come from app/scripts/build_windows.ps1 / build_macos.sh. */
const packageExt: Record<Platform, string> = { android: '.apk', ios: '.ipa', windows: '.zip', macos: '.zip' }
const platformIcon = (p: Platform) =>
  p === 'ios' ? <AppleFilled style={{ fontSize: 18 }} />
    : p === 'windows' ? <WindowsFilled style={{ fontSize: 18, color: '#0078d4' }} />
      : p === 'macos' ? <AppleOutlined style={{ fontSize: 18 }} />
      : <AndroidFilled style={{ fontSize: 18, color: '#3ddc84' }} />

const abiOptions = [
  { value: 'arm64-v8a', label: '64 位（arm64-v8a，绝大多数手机）' },
  { value: 'armeabi-v7a', label: '32 位（armeabi-v7a，老款手机）' },
  { value: '', label: '通用（包含所有架构）' },
  { value: 'x86_64', label: 'x86_64（模拟器 / 少数平板）' },
]
const abiShort: Record<string, string> = { 'arm64-v8a': '64 位', 'armeabi-v7a': '32 位', x86_64: 'x86_64', '': '通用' }
/** Flutter --split-per-abi output names end in -<abi>-release.apk. */
const abiFromName = (name: string) => ['arm64-v8a', 'armeabi-v7a', 'x86_64'].find((a) => name.includes(a)) ?? ''

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
    setSource(p === 'ios' ? 'url' : 'file')
    setFile([])
    setProgress(undefined)
    form.resetFields()
    form.setFieldsValue({
      version: bump(l?.version),
      build: (l?.build ?? 0) + 1,
      notes: '',
      force: false,
      bundle_id: p === 'ios' ? (l?.bundle_id || 'app.cygnus6152.artichoke66') : '',
      abi: p === 'android' ? 'arm64-v8a' : '',
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
    if (platform === 'android') fd.append('abi', v.abi ?? '')
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
      content: `删除 ${platformLabel[r.platform]} ${r.version} (${r.build})？安装包文件也会删除。`,
      okButtonProps: { danger: true },
      onOk: async () => {
        if (await run(() => api.releases.remove(r.id), '已删除')) reload()
      },
    })

  const uploading = progress !== undefined
  const fileName = file[0]?.name
  const wantExt = packageExt[platform]

  return (
    <div className="page">
      <PageCard>
        <div className="toolbar">
          <span className="muted">App 启动和「我的 → 检查更新」读取每个平台最新的已发布版本；标记「强制更新」后，旧版本必须升级才能继续使用</span>
          <div className="spacer" />
          <Button icon={<ReloadOutlined />} onClick={() => reload()}>刷新</Button>
          <Button icon={<AppleOutlined />} onClick={() => openCreate('macos')}>发布 macOS 版本</Button>
          <Button icon={<WindowsFilled />} onClick={() => openCreate('windows')}>发布 Windows 版本</Button>
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
                  {platformIcon(r.platform)}
                  <div>
                    <div style={{ fontWeight: 600 }}>
                      {r.version} <span className="muted">build {r.build}</span>
                      {r.platform === 'android' && <Tag bordered={false} style={{ marginLeft: 6 }}>{abiShort[r.abi ?? ''] ?? r.abi}</Tag>}
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
        title={`发布 ${platformLabel[platform]} 版本`}
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
              tooltip="与 pubspec.yaml 里 version 的 + 后面一致（1.2.0+5 的 5）。新版本要比上一版大；同一版本的不同 CPU 架构用相同构建号"
              rules={[{ required: true, message: '必填' }]}
            >
              <InputNumber min={1} style={{ width: 130 }} />
            </Form.Item>
          </Space>

          {platform === 'android' && (
            <Form.Item
              name="abi"
              label="CPU 架构"
              extra="用 flutter build apk --split-per-abi 打出的每个 APK 分别发布一次（同一构建号）；App 会按手机的 CPU 自动拿到对应的包"
            >
              <Select options={abiOptions} />
            </Form.Item>
          )}
          <Form.Item label="安装包来源">
            <Segmented
              value={source}
              onChange={(v) => setSource(v as 'file' | 'url')}
              options={[
                { label: `上传 ${wantExt.slice(1).toUpperCase()}`, value: 'file' },
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
                  onChange={({ fileList }) => {
                    const f = fileList.slice(-1)
                    setFile(f)
                    if (platform === 'android' && f[0]) form.setFieldValue('abi', abiFromName(f[0].name))
                  }}
                  disabled={uploading}
                >
                  <p className="ant-upload-drag-icon"><InboxOutlined /></p>
                  <p className="ant-upload-text">{fileName ?? `点击或拖拽 ${wantExt} 到这里`}</p>
                  <p className="ant-upload-hint">最大 600 MB，服务器会计算 SHA-256 供 App 校验</p>
                </Upload.Dragger>
              </Form.Item>
              {platform === 'windows' && (
                <Alert
                  type="info"
                  showIcon
                  style={{ marginBottom: 16 }}
                  message="在 Windows 上运行 app/scripts/build_windows.ps1 打出的 zip（含 Huanyou.exe、huanyou-core.exe 和运行库）。用户在下载页下载后解压运行。"
                />
              )}
              {platform === 'macos' && (
                <Alert
                  type="info"
                  showIcon
                  style={{ marginBottom: 16 }}
                  message="在 Mac 上运行 app/scripts/build_macos.sh 打出的 zip（内含 环游.app）。用 Developer ID 签名并公证后，用户打开时不会被系统拦截。"
                />
              )}
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
