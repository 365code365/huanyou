'use client'
import { CopyOutlined, DownloadOutlined, PlusOutlined, ReloadOutlined, RobotOutlined, UploadOutlined } from '@ant-design/icons'
import {
  Alert,
  App,
  AutoComplete,
  Button,
  Collapse,
  Form,
  Input,
  Modal,
  Progress,
  Segmented,
  Space,
  Switch,
  Table,
  Tabs,
  Tag,
  Typography,
  Upload,
} from 'antd'
import { useMemo, useState } from 'react'
import { PageCard } from '@/components/PageCard'
import { useAction, useAsync } from '@/hooks/useAsync'
import { api, type LangPack, type SuggestedLang } from '@/lib/api'
import { dateTime, errMsg } from '@/lib/format'

const BATCH = 60
const placeholders = (s: string) => (s.match(/\{\d+\}/g) ?? []).sort().join(',')

interface Draft {
  code: string
  name: string
  native_name: string
  rtl: boolean
  enabled: boolean
  strings: Record<string, string>
  isNew: boolean
}

function saveFile(name: string, text: string) {
  const a = document.createElement('a')
  a.href = URL.createObjectURL(new Blob([text], { type: 'application/json' }))
  a.download = name
  a.click()
  setTimeout(() => URL.revokeObjectURL(a.href), 1000)
}

export default function I18nPage() {
  const { message, modal } = App.useApp()
  const { data, loading, reload, setData } = useAsync(api.i18n.list)
  const { run, busy } = useAction()
  const [draft, setDraft] = useState<Draft>()
  const [keys, setKeys] = useState<string[]>([])
  const [prompt, setPrompt] = useState('')
  const [paste, setPaste] = useState('')
  const [progress, setProgress] = useState<number>()
  const [filter, setFilter] = useState<'all' | 'missing' | 'bad'>('all')
  const [q, setQ] = useState('')
  const [aiForm] = Form.useForm()
  const base = data?.base_count ?? 0
  const suggested = useMemo(
    () => (data?.suggested ?? []).filter((s) => !data?.languages.some((l) => l.code === s.code)),
    [data],
  )

  const loadBase = async (code: string, name: string) => {
    const b = await api.i18n.base(code, name)
    setKeys(b.keys)
    setPrompt(b.prompt)
    return b
  }

  const openNew = async (s?: SuggestedLang) => {
    setPaste('')
    setFilter('all')
    setQ('')
    setDraft({ code: s?.code ?? '', name: s?.name ?? '', native_name: s?.native_name ?? '', rtl: s?.rtl ?? false, enabled: true, strings: {}, isNew: true })
    await loadBase(s?.code ?? '', s?.native_name ?? '')
  }

  const openEdit = async (l: LangPack) => {
    try {
      const [full] = await Promise.all([api.i18n.get(l.code), loadBase(l.code, l.native_name)])
      setPaste('')
      setFilter('all')
      setQ('')
      setDraft({ ...full, strings: full.strings ?? {}, isNew: false })
    } catch (e) {
      message.error(errMsg(e))
    }
  }

  const patchDraft = (p: Partial<Draft>) => setDraft((d) => d && { ...d, ...p })
  const merge = (m: Record<string, string>) => setDraft((d) => d && { ...d, strings: { ...d.strings, ...m } })

  const done = draft ? keys.filter((k) => draft.strings[k]).length : 0
  const bad = useMemo(
    () => (draft ? keys.filter((k) => draft.strings[k] && placeholders(k) !== placeholders(draft.strings[k])) : []),
    [draft, keys],
  )
  const rows = useMemo(() => {
    if (!draft) return []
    const badSet = new Set(bad)
    return keys
      .filter((k) => (filter === 'missing' ? !draft.strings[k] : filter === 'bad' ? badSet.has(k) : true))
      .filter((k) => !q || k.includes(q) || (draft.strings[k] ?? '').toLowerCase().includes(q.toLowerCase()))
  }, [draft, keys, filter, q, bad])

  const templateText = () => JSON.stringify(Object.fromEntries(keys.map((k) => [k, draft?.strings[k] ?? ''])), null, 2)

  const copyPrompt = async (onlyMissing: boolean) => {
    const list = onlyMissing ? keys.filter((k) => !draft?.strings[k]) : keys
    await navigator.clipboard.writeText(`${prompt}\n\n${JSON.stringify(list, null, 1)}`)
    message.success(`已复制提示词和 ${list.length} 条原文，粘贴到任意 AI 对话里，再把返回的 JSON 贴回下方`)
  }

  const parsePaste = async (text = paste) => {
    if (!text.trim()) return
    try {
      const r = await api.i18n.parse(text)
      const known = new Set(keys)
      const got = Object.fromEntries(Object.entries(r.strings).filter(([k, v]) => known.has(k) && v))
      merge(got)
      const unknown = Object.keys(r.strings).length - Object.keys(got).length
      message.success(`已合并 ${Object.keys(got).length} 条${unknown > 0 ? `，忽略 ${unknown} 条不在模板里的` : ''}`)
      setPaste('')
    } catch (e) {
      message.error(errMsg(e))
    }
  }

  const aiTranslate = async (onlyMissing: boolean) => {
    if (!draft) return
    const todo = onlyMissing ? keys.filter((k) => !draft.strings[k]) : keys
    if (!todo.length) return message.info('没有需要翻译的条目')
    setProgress(0)
    let failed = 0
    for (let i = 0; i < todo.length; i += BATCH) {
      const batch = todo.slice(i, i + BATCH)
      try {
        const r = await api.i18n.translate(draft.code, draft.native_name || draft.name, batch)
        merge(r.strings)
      } catch (e) {
        failed += batch.length
        message.error(errMsg(e))
        if (i === 0) break // config problem: don't hammer the API
      }
      setProgress(Math.min(1, (i + BATCH) / todo.length))
    }
    setProgress(undefined)
    if (!failed) message.success('翻译完成，检查后点「保存」')
  }

  const save = async () => {
    if (!draft) return
    if (!/^[a-z]{2,3}(-[A-Za-z0-9]{2,8})?$/.test(draft.code)) return message.warning('语言代码格式如 en、es、pt-BR')
    if (!draft.native_name.trim()) return message.warning('请填写本地名称（App 里显示）')
    const doSave = () =>
      run(async () => {
        const r = await api.i18n.save(draft)
        setData((d) => d && { ...d, languages: [...d.languages.filter((l) => l.code !== r.language.code), r.language] })
        setDraft(undefined)
        reload(true)
      }, '已保存，App 下次启动或切换语言时会下载新版本')
    if (bad.length) {
      modal.confirm({
        title: '占位符不一致',
        content: `有 ${bad.length} 条译文的 {0} 这类占位符和原文不一致，App 里会显示错误。仍然保存？`,
        okText: '仍然保存',
        onOk: doSave,
      })
    } else doSave()
  }

  const toggle = async (l: LangPack, enabled: boolean) => {
    const full = await api.i18n.get(l.code)
    if (await run(() => api.i18n.save({ ...full, strings: full.strings ?? {}, enabled }), enabled ? '已启用' : '已停用')) reload(true)
  }

  const remove = (l: LangPack) =>
    modal.confirm({
      title: `删除 ${l.native_name}`,
      content: '删除后 App 里不再提供该语言，正在使用的用户回到跟随系统（无匹配时显示中文）。',
      okButtonProps: { danger: true },
      onOk: async () => {
        if (await run(() => api.i18n.remove(l.code), '已删除')) reload(true)
      },
    })

  const exportPack = async (l: LangPack) => {
    const full = await api.i18n.get(l.code)
    saveFile(`${l.code}.json`, JSON.stringify(full.strings ?? {}, null, 2))
  }

  const saveAI = async () => {
    const v = await aiForm.validateFields()
    await run(async () => {
      const ai = await api.i18n.saveAI(v)
      setData((d) => d && { ...d, ai })
    }, '已保存 AI 接口')
  }

  const ai = data?.ai
  const translating = progress !== undefined

  return (
    <div className="page">
      <PageCard>
        <div className="toolbar">
          <span className="muted">
            App 内置简体中文，其他语言以语言包下载。App 默认跟随手机系统语言，没有对应语言包时用英语，再没有用中文；用户可在「我的 → 语言」切换。
          </span>
          <div className="spacer" />
          <Button icon={<ReloadOutlined />} onClick={() => reload()}>刷新</Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => openNew()}>添加语言</Button>
        </div>
        {suggested.length > 0 && (
          <div style={{ marginBottom: 14 }}>
            <span className="muted" style={{ marginRight: 8 }}>快速添加：</span>
            {suggested.map((s) => (
              <Tag key={s.code} style={{ cursor: 'pointer', marginBottom: 6 }} onClick={() => openNew(s)}>
                <PlusOutlined /> {s.name} <span className="muted">{s.region}</span>
              </Tag>
            ))}
          </div>
        )}
        <Table<LangPack>
          rowKey="code"
          loading={loading}
          dataSource={data?.languages ?? []}
          pagination={false}
          locale={{ emptyText: '还没有语言包，点上方「快速添加」开始' }}
          columns={[
            {
              title: '语言',
              render: (_, l) => (
                <div>
                  <span style={{ fontWeight: 600 }}>{l.native_name}</span> <span className="muted">{l.name}</span>
                  <div className="muted mono">
                    {l.code} {l.rtl && <Tag bordered={false} color="purple">从右到左</Tag>}
                  </div>
                </div>
              ),
            },
            {
              title: '翻译进度',
              width: 220,
              render: (_, l) => (
                <Progress
                  percent={base ? Math.min(100, Math.round((l.count / base) * 100)) : 0}
                  size="small"
                  format={() => `${Math.min(l.count, base)}/${base}`}
                />
              ),
            },
            { title: '版本', width: 170, render: (_, l) => <span className="muted">{dateTime(l.updated_at)}</span> },
            { title: '启用', width: 70, render: (_, l) => <Switch size="small" checked={l.enabled} onChange={(v) => toggle(l, v)} /> },
            {
              title: '操作',
              width: 170,
              render: (_, l) => (
                <Space size={0}>
                  <Button type="link" size="small" onClick={() => openEdit(l)}>编辑</Button>
                  <Button type="link" size="small" onClick={() => exportPack(l)}>导出</Button>
                  <Button type="link" size="small" danger onClick={() => remove(l)}>删除</Button>
                </Space>
              ),
            },
          ]}
        />
      </PageCard>

      <PageCard style={{ marginTop: 16 }}>
        <Collapse
          ghost
          items={[
            {
              key: 'ai',
              label: (
                <Space>
                  <RobotOutlined /> AI 自动翻译接口
                  {ai?.ready ? <Tag color="success" bordered={false}>已配置 · {ai.model}</Tag> : <Tag bordered={false}>未配置（可用复制提示词手动翻译）</Tag>}
                </Space>
              ),
              children: (
                <Form
                  form={aiForm}
                  layout="vertical"
                  requiredMark={false}
                  style={{ maxWidth: 560 }}
                  initialValues={{ base_url: ai?.base_url || 'https://api.openai.com/v1', model: ai?.model || 'gpt-4o-mini', api_key: ai?.api_key }}
                  key={ai?.api_key ?? 'none'}
                >
                  <Form.Item name="base_url" label="接口地址" extra="任何 OpenAI 兼容的 /chat/completions 接口：OpenAI、DeepSeek、通义千问、Moonshot 等">
                    <AutoComplete
                      options={[
                        { value: 'https://api.openai.com/v1' },
                        { value: 'https://api.deepseek.com/v1' },
                        { value: 'https://dashscope.aliyuncs.com/compatible-mode/v1' },
                        { value: 'https://api.moonshot.cn/v1' },
                      ]}
                    />
                  </Form.Item>
                  <Form.Item name="model" label="模型" rules={[{ required: true, message: '必填' }]}>
                    <AutoComplete options={[{ value: 'gpt-4o-mini' }, { value: 'deepseek-chat' }, { value: 'qwen-plus' }, { value: 'moonshot-v1-32k' }]} />
                  </Form.Item>
                  <Form.Item name="api_key" label="API Key" extra="保存在服务器数据库，页面只显示脱敏值；不改就保持原值">
                    <Input.Password autoComplete="off" />
                  </Form.Item>
                  <Button type="primary" loading={busy} onClick={saveAI}>保存</Button>
                </Form>
              ),
            },
          ]}
        />
      </PageCard>

      <Modal
        open={!!draft}
        title={draft?.isNew ? '添加语言' : `编辑 ${draft?.native_name}`}
        width={980}
        onCancel={() => !translating && setDraft(undefined)}
        maskClosable={false}
        okText="保存"
        onOk={save}
        okButtonProps={{ loading: busy, disabled: translating }}
        destroyOnHidden
      >
        {draft && (
          <>
            <Space wrap size={12} style={{ marginBottom: 12 }}>
              <Input addonBefore="代码" style={{ width: 190 }} value={draft.code} disabled={!draft.isNew} placeholder="en / pt-BR"
                onChange={(e) => patchDraft({ code: e.target.value.trim() })} />
              <Input addonBefore="本地名称" style={{ width: 240 }} value={draft.native_name} placeholder="English"
                onChange={(e) => patchDraft({ native_name: e.target.value })} />
              <Input addonBefore="中文名" style={{ width: 200 }} value={draft.name} placeholder="英语" onChange={(e) => patchDraft({ name: e.target.value })} />
              <Space size={4}><Switch size="small" checked={draft.rtl} onChange={(v) => patchDraft({ rtl: v })} /> 从右到左</Space>
              <Space size={4}><Switch size="small" checked={draft.enabled} onChange={(v) => patchDraft({ enabled: v })} /> 启用</Space>
            </Space>
            <Progress percent={keys.length ? Math.round((done / keys.length) * 100) : 0} format={() => `已翻译 ${done}/${keys.length}`} />
            {bad.length > 0 && (
              <Alert type="warning" showIcon style={{ margin: '8px 0' }} message={`${bad.length} 条译文的占位符（{0}、{1}…）和原文不一致`}
                action={<Button size="small" onClick={() => setFilter('bad')}>查看</Button>} />
            )}
            <Tabs
              style={{ marginTop: 8 }}
              items={[
                {
                  key: 'paste',
                  label: '粘贴 / 上传 JSON',
                  children: (
                    <>
                      <Space wrap style={{ marginBottom: 10 }}>
                        <Button icon={<CopyOutlined />} onClick={() => copyPrompt(true)}>复制 AI 提示词（缺失 {keys.length - done} 条）</Button>
                        <Button icon={<CopyOutlined />} onClick={() => copyPrompt(false)}>复制 AI 提示词（全部）</Button>
                        <Button icon={<DownloadOutlined />} onClick={() => saveFile(`${draft.code || 'template'}.json`, templateText())}>下载模板</Button>
                        <Upload accept=".json,application/json" showUploadList={false}
                          beforeUpload={(f) => { f.text().then((t) => parsePaste(t)); return false }}>
                          <Button icon={<UploadOutlined />}>上传 JSON 文件</Button>
                        </Upload>
                      </Space>
                      <Input.TextArea
                        rows={12}
                        className="mono"
                        value={paste}
                        onChange={(e) => setPaste(e.target.value)}
                        placeholder={'把 AI 返回的 JSON 粘贴到这里，格式：\n{\n  "连接": "Connect",\n  "剩余 {0} 天": "{0} days left"\n}\n多余的说明文字、```json 代码块都会自动去掉'}
                      />
                      <Button type="primary" style={{ marginTop: 10 }} disabled={!paste.trim()} onClick={() => parsePaste()}>解析并合并</Button>
                    </>
                  ),
                },
                {
                  key: 'ai',
                  label: 'AI 自动翻译',
                  children: ai?.ready ? (
                    <Space direction="vertical" style={{ width: '100%' }}>
                      <span className="muted">使用 {ai.model} 按每批 {BATCH} 条翻译成「{draft.native_name || draft.name || draft.code}」，结果合并到当前草稿，保存前可在「逐条校对」修改。</span>
                      <Space>
                        <Button type="primary" icon={<RobotOutlined />} loading={translating} disabled={!draft.code} onClick={() => aiTranslate(true)}>翻译缺失项</Button>
                        <Button disabled={translating || !draft.code} onClick={() => aiTranslate(false)}>全部重新翻译</Button>
                      </Space>
                      {translating && <Progress percent={Math.round((progress ?? 0) * 100)} status="active" />}
                    </Space>
                  ) : (
                    <Alert type="info" showIcon message="还没有配置 AI 接口" description="在本页下方「AI 自动翻译接口」填写接口地址、模型和 API Key；或者用「粘贴 / 上传 JSON」页复制提示词到任意 AI 对话里翻译。" />
                  ),
                },
                {
                  key: 'edit',
                  label: '逐条校对',
                  children: (
                    <>
                      <Space style={{ marginBottom: 10 }}>
                        <Segmented value={filter} onChange={(v) => setFilter(v as typeof filter)}
                          options={[{ label: '全部', value: 'all' }, { label: `缺失 ${keys.length - done}`, value: 'missing' }, { label: `占位符错误 ${bad.length}`, value: 'bad' }]} />
                        <Input.Search allowClear placeholder="搜索原文或译文" style={{ width: 240 }} onChange={(e) => setQ(e.target.value)} />
                      </Space>
                      <Table<string>
                        rowKey={(k) => k}
                        size="small"
                        dataSource={rows}
                        pagination={{ pageSize: 50, showSizeChanger: false }}
                        scroll={{ y: 420 }}
                        columns={[
                          { title: '中文原文', width: '45%', render: (_, k) => <Typography.Text style={{ whiteSpace: 'pre-wrap' }}>{k}</Typography.Text> },
                          {
                            title: '译文',
                            render: (_, k) => (
                              <Input.TextArea
                                autoSize={{ minRows: 1, maxRows: 4 }}
                                dir={draft.rtl ? 'rtl' : 'ltr'}
                                status={draft.strings[k] && placeholders(k) !== placeholders(draft.strings[k]) ? 'warning' : undefined}
                                value={draft.strings[k] ?? ''}
                                onChange={(e) => merge({ [k]: e.target.value })}
                              />
                            ),
                          },
                        ]}
                      />
                    </>
                  ),
                },
              ]}
            />
          </>
        )}
      </Modal>
    </div>
  )
}
