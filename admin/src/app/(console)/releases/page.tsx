'use client'
import { AndroidFilled, AppleFilled, AppleOutlined, CloudUploadOutlined, DownOutlined, InboxOutlined, LinkOutlined, QuestionCircleOutlined, ReloadOutlined, WindowsFilled } from '@ant-design/icons'
import { Alert, App, Button, Dropdown, Form, Input, InputNumber, Modal, Progress, Segmented, Select, Space, Switch, Table, Tag, Tooltip, Upload } from 'antd'
import type { UploadFile } from 'antd'
import type { ReactNode } from 'react'
import { useEffect, useMemo, useState } from 'react'
import { PageCard } from '@/components/PageCard'
import { useAction, useAsync } from '@/hooks/useAsync'
import { api, type Release } from '@/lib/api'
import { bytes, date, errMsg } from '@/lib/format'

type Platform = Release['platform']

interface PlatformSpec {
  label: string
  icon: ReactNode
  /** Package types accepted for this platform (first = main). */
  exts: string[]
  /** The server reads version, build and CPU from the uploaded package (server/internal/pkginfo). */
  auto: boolean
  /** CPU builds; value "" = universal. Must match `platforms` in server/internal/api/releases.go. */
  abis: { value: string; label: string; short: string }[]
  defaultAbi: string
  /** Shown under the file picker. */
  hint?: string
}

const iconStyle = { fontSize: 18 }
const platforms: Record<Platform, PlatformSpec> = {
  android: {
    label: 'Android',
    icon: <AndroidFilled style={{ ...iconStyle, color: '#3ddc84' }} />,
    exts: ['.apk'],
    auto: false,
    abis: [
      { value: 'arm64-v8a', label: '64 位（arm64-v8a，绝大多数手机）', short: '64 位' },
      { value: 'armeabi-v7a', label: '32 位（armeabi-v7a，老款手机）', short: '32 位' },
      { value: '', label: '通用（包含所有架构）', short: '通用' },
      { value: 'x86_64', label: 'x86_64（模拟器 / 少数平板）', short: 'x86_64' },
    ],
    defaultAbi: 'arm64-v8a',
  },
  ios: {
    label: 'iOS',
    icon: <AppleFilled style={iconStyle} />,
    exts: ['.ipa'],
    auto: false,
    abis: [],
    defaultAbi: '',
    hint: 'IPA 需为 Ad-hoc / 企业签名。下载页会生成 itms-services 安装链接，测试设备用 Safari 打开即可安装（设备需在描述文件里）。',
  },
  windows: {
    label: 'Windows',
    icon: <WindowsFilled style={{ ...iconStyle, color: '#0078d4' }} />,
    exts: ['.exe', '.zip'],
    auto: true,
    abis: [
      { value: 'x86_64', label: 'x64（Intel / AMD）', short: 'x64' },
      { value: 'arm64', label: 'ARM64（骁龙等）', short: 'ARM64' },
    ],
    defaultAbi: 'x86_64',
    hint: '安装程序 .exe：Inno Setup 打出的 setup.exe，用户下载后双击安装，App 内更新会静默安装。版本号和构建号读取 .iss 的 VersionInfoVersion（写成 1.2.7.12），没设置时按文件名识别，如 huanyou-windows-1.2.7-12-x64-setup.exe（arm64 写在文件名里）。\n压缩包 .zip：app/scripts/build_windows.ps1 打出的包（含 Huanyou.exe、huanyou-core.exe 和运行库），用户下载后解压运行。',
  },
  macos: {
    label: 'macOS',
    icon: <AppleOutlined style={iconStyle} />,
    exts: ['.dmg'],
    auto: true,
    abis: [
      { value: '', label: '通用（Apple 芯片 + Intel）', short: '通用' },
      { value: 'arm64', label: 'Apple 芯片（M 系列）', short: 'Apple 芯片' },
      { value: 'x86_64', label: 'Intel', short: 'Intel' },
    ],
    defaultAbi: '',
    hint: '上传 app/scripts/build_macos.sh 打出的 dmg（打开后把 环游 拖进「应用程序」）。用 Developer ID 签名并公证后，用户打开时不会被系统拦截。',
  },
}
/** Toolbar order, primary button last. */
const order: Platform[] = ['macos', 'windows', 'ios', 'android']

const abiShort = (r: Release) => platforms[r.platform].abis.find((a) => a.value === (r.abi ?? ''))?.short ?? r.abi
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

  // Platform tab ("all" = every platform), remembered across visits.
  const [tab, setTabState] = useState<Platform | 'all'>('all')
  useEffect(() => {
    const t = localStorage.getItem('releases_tab') as Platform | 'all' | null
    if (t && (t === 'all' || t in platforms)) setTabState(t)
  }, [])
  const setTab = (t: Platform | 'all') => {
    setTabState(t)
    localStorage.setItem('releases_tab', t)
  }
  const shown = useMemo(() => (tab === 'all' ? items : items.filter((r) => r.platform === tab)), [items, tab])
  const tabOrder: Platform[] = ['android', 'ios', 'macos', 'windows']

  const [open, setOpen] = useState(false)
  const [platform, setPlatform] = useState<Platform>('android')
  const [source, setSource] = useState<'file' | 'url'>('file')
  const [file, setFile] = useState<UploadFile[]>([])
  const [progress, setProgress] = useState<number>()
  const [form] = Form.useForm()

  const spec = platforms[platform]
  /** Version, build and CPU come from the package: nothing to type in. */
  const auto = spec.auto && source === 'file'
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
      abi: platforms[p].defaultAbi,
    })
    setOpen(true)
  }

  const submit = async () => {
    const v = await form.validateFields()
    if (source === 'file' && !file[0]?.originFileObj) return message.warning('请选择安装包')
    const fd = new FormData()
    // Text fields first: the server streams the file and needs the platform before it.
    fd.append('platform', platform)
    if (!auto) {
      fd.append('version', v.version.trim())
      fd.append('build', String(v.build))
      if (spec.abis.length) fd.append('abi', v.abi ?? '')
    }
    fd.append('notes', v.notes ?? '')
    fd.append('force', String(!!v.force))
    if (platform === 'ios') fd.append('bundle_id', v.bundle_id ?? '')
    if (source === 'url') fd.append('url', v.url.trim())
    else fd.append('file', file[0].originFileObj as Blob, file[0].name)
    setProgress(0)
    try {
      const r = await api.releases.create(fd, setProgress)
      const cpu = spec.abis.length ? ` · ${abiShort(r)}` : ''
      message.success(
        auto
          ? `已发布：识别为 ${r.version}（构建 ${r.build}）${cpu}，App 内检查更新即可看到`
          : '已发布，App 内检查更新即可看到',
      )
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
      content: `删除 ${platforms[r.platform].label} ${r.version} (${r.build})？安装包文件也会删除。`,
      okButtonProps: { danger: true },
      onOk: async () => {
        if (await run(() => api.releases.remove(r.id), '已删除')) reload()
      },
    })

  const uploading = progress !== undefined
  const fileName = file[0]?.name

  return (
    <div className="page">
      <PageCard>
        <div className="toolbar">
          <Segmented
            value={tab}
            onChange={(v) => setTab(v as Platform | 'all')}
            options={[
              { label: `全部 ${items.length}`, value: 'all' },
              ...tabOrder.map((p) => ({
                value: p,
                label: (
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                    {platforms[p].icon}
                    {platforms[p].label} {items.filter((r) => r.platform === p).length}
                  </span>
                ),
              })),
            ]}
          />
          <Tooltip title="App 启动和「我的 → 检查更新」读取每个平台最新的已发布版本；标记「强制更新」后，旧版本必须升级才能继续使用">
            <QuestionCircleOutlined className="muted" style={{ cursor: 'help' }} />
          </Tooltip>
          <div className="spacer" />
          <Button icon={<ReloadOutlined />} onClick={() => reload()}>刷新</Button>
          {tab === 'all' ? (
            <Dropdown
              menu={{
                items: order.slice().reverse().map((p) => ({ key: p, icon: platforms[p].icon, label: `发布 ${platforms[p].label} 版本` })),
                onClick: ({ key }) => openCreate(key as Platform),
              }}
            >
              <Button type="primary" icon={<CloudUploadOutlined />}>发布新版本 <DownOutlined /></Button>
            </Dropdown>
          ) : (
            <Button type="primary" icon={platforms[tab].icon} onClick={() => openCreate(tab)}>发布 {platforms[tab].label} 版本</Button>
          )}
        </div>
        <Table<Release>
          rowKey="id"
          loading={loading}
          dataSource={shown}
          pagination={false}
          locale={{ emptyText: tab === 'all' ? '还没有发布版本' : `还没有发布 ${platforms[tab].label} 版本` }}
          columns={[
            {
              title: '版本',
              render: (_, r) => (
                <Space>
                  {platforms[r.platform].icon}
                  <div>
                    <div style={{ fontWeight: 600 }}>
                      {r.version} <span className="muted">build {r.build}</span>
                      {platforms[r.platform].abis.length > 0 && <Tag bordered={false} style={{ marginLeft: 6 }}>{abiShort(r)}</Tag>}
                      {r.platform === 'windows' && (r.kind === 'exe' || r.kind === 'zip') && (
                        <Tag bordered={false} color={r.kind === 'exe' ? 'blue' : undefined} style={{ marginLeft: 6 }}>
                          {r.kind === 'exe' ? '安装程序' : '压缩包'}
                        </Tag>
                      )}
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
        title={`发布 ${spec.label} 版本`}
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
          <Form.Item label="安装包来源">
            <Segmented
              value={source}
              onChange={(v) => setSource(v as 'file' | 'url')}
              options={[
                { label: `上传 ${spec.exts.map((e) => e.slice(1).toUpperCase()).join(' / ')}`, value: 'file' },
                { label: platform === 'ios' ? 'TestFlight / App Store 链接' : '外部链接', value: 'url' },
              ]}
            />
          </Form.Item>

          {auto ? (
            <Alert
              type="success"
              showIcon
              style={{ marginBottom: 16 }}
              message={
                platform === 'windows'
                  ? '版本号、构建号和支持的芯片会从安装包里自动读取。zip 会一并校验是否包含 VPN 核心；exe 安装程序的内容是压缩的，无法检查，请确认打包时带上了 huanyou-core.exe。'
                  : '版本号、构建号和支持的芯片会从安装包里自动读取，上传时一并校验安装包是否完整（含 VPN 核心）。'
              }
            />
          ) : (
            <>
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
              {spec.abis.length > 0 && (
                <Form.Item
                  name="abi"
                  label="CPU 架构"
                  extra={
                    platform === 'android'
                      ? '用 flutter build apk --split-per-abi 打出的每个 APK 分别发布一次（同一构建号）；App 会按手机的 CPU 自动拿到对应的包'
                      : '外部链接指向的安装包支持的芯片'
                  }
                >
                  <Select options={spec.abis} />
                </Form.Item>
              )}
            </>
          )}

          {source === 'file' ? (
            <>
              <Form.Item>
                <Upload.Dragger
                  accept={spec.exts.join(',')}
                  maxCount={1}
                  fileList={file}
                  beforeUpload={(f) => {
                    if (!spec.exts.some((e) => f.name.toLowerCase().endsWith(e))) {
                      message.error(`请选择 ${spec.exts.join(' 或 ')} 文件`)
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
                  <p className="ant-upload-text">{fileName ?? `点击或拖拽 ${spec.exts.join(' / ')} 到这里`}</p>
                  <p className="ant-upload-hint">最大 600 MB，服务器会计算 SHA-256 供 App 校验</p>
                </Upload.Dragger>
              </Form.Item>
              {platform === 'ios' && (
                <Form.Item name="bundle_id" label="Bundle ID" rules={[{ required: true, message: '必填' }]}>
                  <Input className="mono" />
                </Form.Item>
              )}
              {spec.hint && <Alert type="info" showIcon style={{ marginBottom: 16, whiteSpace: 'pre-line' }} message={spec.hint} />}
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
