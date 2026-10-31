'use client'
import {
  ClearOutlined,
  DownloadOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  RobotOutlined,
  SearchOutlined,
  VerticalAlignBottomOutlined,
} from '@ant-design/icons'
import { Alert, App, Button, Col, Input, Row, Segmented, Space, Spin, Switch, Tag, Tooltip } from 'antd'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Markdown } from '@/components/Markdown'
import { PageCard } from '@/components/PageCard'
import { api, type LogEntry } from '@/lib/api'
import { errMsg } from '@/lib/format'

const MAX_LINES = 3000
const levelColor: Record<string, string> = { DEBUG: '#8c8c8c', INFO: '#69b1ff', WARN: '#faad14', ERROR: '#ff4d4f' }

const quick = [
  '分析最近的错误和警告：原因是什么，怎么处理？',
  '节点同步和流量上报是否正常？有没有节点失联？',
  '支付相关（下单、通知、主动查询）有没有异常？',
  '有没有可疑的访问或攻击（封禁、扫描、登录失败）？',
]

export default function LogsPage() {
  const { message } = App.useApp()
  const [lines, setLines] = useState<LogEntry[]>([])
  const [level, setLevel] = useState<string>('')
  const [q, setQ] = useState('')
  const [applied, setApplied] = useState({ level: '', q: '' })
  const [paused, setPaused] = useState(false)
  const [follow, setFollow] = useState(true)
  const [online, setOnline] = useState(true)
  const lastId = useRef(0)
  const pausedRef = useRef(false)
  pausedRef.current = paused
  const box = useRef<HTMLDivElement>(null)

  // Live tail: load the recent lines, then long-poll for new ones.
  useEffect(() => {
    let stop = false
    lastId.current = 0
    setLines([])
    const loop = async () => {
      let first = true
      while (!stop) {
        if (pausedRef.current && !first) {
          await new Promise((r) => setTimeout(r, 500))
          continue
        }
        try {
          const r = await api.logs.list({
            after: lastId.current || undefined, level: applied.level, q: applied.q,
            limit: first ? 800 : 500, wait: first ? undefined : 25,
          })
          if (stop) return
          setOnline(true)
          if (pausedRef.current && !first) continue // fetched again after resume
          lastId.current = Math.max(lastId.current, r.last_id)
          if (r.items.length) setLines((prev) => [...prev, ...r.items].slice(-MAX_LINES))
          first = false
        } catch {
          if (stop) return
          setOnline(false)
          await new Promise((r) => setTimeout(r, 3000))
        }
      }
    }
    loop()
    return () => {
      stop = true
    }
  }, [applied])

  useEffect(() => {
    if (follow && box.current) box.current.scrollTop = box.current.scrollHeight
  }, [lines, follow])

  const onScroll = () => {
    const el = box.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40
    if (atBottom !== follow) setFollow(atBottom)
  }

  const counts = useMemo(() => {
    const c = { WARN: 0, ERROR: 0 }
    for (const l of lines) if (l.level === 'WARN' || l.level === 'ERROR') c[l.level]++
    return c
  }, [lines])

  const download = () => {
    const text = lines.map(fmt).join('\n')
    const a = document.createElement('a')
    a.href = URL.createObjectURL(new Blob([text], { type: 'text/plain' }))
    a.download = `vpn-api-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '')}.log`
    a.click()
    URL.revokeObjectURL(a.href)
  }

  // ---- AI ----
  const [question, setQuestion] = useState('')
  const [asking, setAsking] = useState(false)
  const [answer, setAnswer] = useState<{ text: string; lines: number; q: string }>()
  const ask = useCallback(
    async (text?: string) => {
      const qn = (text ?? question).trim() || quick[0]
      setAsking(true)
      try {
        const r = await api.logs.ask({ question: qn, level: applied.level, q: applied.q, limit: 400 })
        setAnswer({ text: r.answer, lines: r.lines, q: qn })
      } catch (e) {
        message.error(errMsg(e))
      } finally {
        setAsking(false)
      }
    },
    [question, applied, message],
  )

  return (
    <div className="page">
      <Row gutter={16}>
        <Col xs={24} xl={15}>
          <PageCard
            title={
              <Space>
                控制台日志
                <Tag color={online ? (paused ? 'default' : 'green') : 'red'} bordered={false}>
                  {online ? (paused ? '已暂停' : '实时') : '连接中断，重试中'}
                </Tag>
              </Space>
            }
            extra={
              <Space size={4}>
                {counts.ERROR > 0 && <Tag color="error" bordered={false}>错误 {counts.ERROR}</Tag>}
                {counts.WARN > 0 && <Tag color="warning" bordered={false}>警告 {counts.WARN}</Tag>}
              </Space>
            }
          >
            <div className="toolbar" style={{ flexWrap: 'wrap', rowGap: 8 }}>
              <Segmented
                value={level}
                onChange={(v) => {
                  setLevel(v as string)
                  setApplied((a) => ({ ...a, level: v as string }))
                }}
                options={[
                  { label: '全部', value: '' },
                  { label: '信息+', value: 'INFO' },
                  { label: '警告+', value: 'WARN' },
                  { label: '仅错误', value: 'ERROR' },
                ]}
              />
              <Input
                allowClear
                prefix={<SearchOutlined />}
                placeholder="搜索：订单号、IP、节点、关键字…（回车）"
                style={{ width: 280 }}
                value={q}
                onChange={(e) => {
                  setQ(e.target.value)
                  if (!e.target.value) setApplied((a) => ({ ...a, q: '' }))
                }}
                onPressEnter={() => setApplied((a) => ({ ...a, q: q.trim() }))}
              />
              <div className="spacer" />
              <Tooltip title={paused ? '继续实时刷新' : '暂停（方便查看）'}>
                <Button icon={paused ? <PlayCircleOutlined /> : <PauseCircleOutlined />} onClick={() => setPaused(!paused)}>
                  {paused ? '继续' : '暂停'}
                </Button>
              </Tooltip>
              <Tooltip title="只清空本页显示，不影响服务器">
                <Button icon={<ClearOutlined />} onClick={() => setLines([])}>清屏</Button>
              </Tooltip>
              <Button icon={<DownloadOutlined />} onClick={download} disabled={!lines.length}>下载</Button>
              <Space size={4} className="muted">
                <Switch size="small" checked={follow} onChange={setFollow} /> 自动滚动
              </Space>
            </div>
            <div ref={box} onScroll={onScroll} className="log-view">
              {lines.length === 0 ? (
                <div className="log-empty">{online ? '暂无日志（只显示服务启动以来的最近 5000 行）' : '无法连接服务器'}</div>
              ) : (
                lines.map((l) => (
                  <div key={l.id} className={'log-line lv-' + l.level} title="点击：让 AI 解释这一行"
                    onClick={() => setQuestion(`这条日志是什么意思？需要处理吗？\n${fmt(l)}`)}>
                    <span className="log-time">{time(l.time)}</span>
                    <span className="log-level" style={{ color: levelColor[l.level] }}>{l.level.padEnd(5)}</span>
                    <span className="log-msg">{l.msg}</span>
                    {l.attrs && <span className="log-attrs"> {l.attrs}</span>}
                  </div>
                ))
              )}
            </div>
            {!follow && (
              <Button size="small" icon={<VerticalAlignBottomOutlined />} style={{ marginTop: 8 }} onClick={() => setFollow(true)}>
                回到最新
              </Button>
            )}
          </PageCard>
        </Col>
        <Col xs={24} xl={9}>
          <PageCard title={<Space><RobotOutlined /> AI 日志分析</Space>}>
            <p className="muted" style={{ marginTop: 0 }}>
              把当前筛选条件下最近 400 行日志发给 AI（「多语言」页配置的 AI 接口）。点左侧任意一行可以让 AI 解释它。
            </p>
            <Space wrap size={[6, 6]} style={{ marginBottom: 10 }}>
              {quick.map((t) => (
                <Button key={t} size="small" disabled={asking} onClick={() => { setQuestion(t); ask(t) }}>
                  {t.split('：')[0].split('？')[0]}
                </Button>
              ))}
            </Space>
            <Input.TextArea
              rows={4}
              value={question}
              onChange={(e) => setQuestion(e.target.value)}
              placeholder="例如：为什么用户连不上？昨晚 23 点之后的错误是什么原因？"
              maxLength={2000}
            />
            <Button type="primary" icon={<RobotOutlined />} loading={asking} style={{ marginTop: 10 }} onClick={() => ask()}>
              问 AI
            </Button>
            <div style={{ marginTop: 16 }}>
              {asking && <Spin tip="AI 正在阅读日志…"><div style={{ height: 60 }} /></Spin>}
              {!asking && answer && (
                <>
                  <Alert type="info" showIcon style={{ marginBottom: 10 }} message={`基于 ${answer.lines} 行日志：${answer.q.split('\n')[0]}`} />
                  <div className="ai-answer"><Markdown text={answer.text} /></div>
                </>
              )}
            </div>
          </PageCard>
        </Col>
      </Row>
    </div>
  )
}

function time(iso: string) {
  const d = new Date(iso)
  const p = (n: number, w = 2) => String(n).padStart(w, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

function fmt(l: LogEntry) {
  return `${time(l.time)} ${l.level} ${l.msg}${l.attrs ? ' ' + l.attrs : ''}`
}
