'use client'
import { BugOutlined, EyeOutlined, LineChartOutlined, ReloadOutlined, TeamOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { App, Button, Col, Drawer, Row, Segmented, Space, Switch, Table, Tag, Typography } from 'antd'
import { useState } from 'react'
import { BarChart } from '@/components/BarChart'
import { PageCard } from '@/components/PageCard'
import { StatCard } from '@/components/StatCard'
import { useAction, useAsync } from '@/hooks/useAsync'
import { api, type Crash, type DayStat } from '@/lib/api'
import { dateTime, errMsg } from '@/lib/format'
import { colors } from '@/lib/theme'

/** Names of the events the app sends (app/lib/core/analytics.dart). */
const eventLabel: Record<string, string> = {
  app_open: '打开 App',
  app_resume: '回到前台',
  vpn_connect: '点击连接',
  vpn_connected: '连接成功',
  vpn_connect_failed: '连接失败',
  vpn_disconnect: '断开连接',
  node_list: '打开节点列表',
  node_select: '切换节点',
  mode_direct: '切到智能模式',
  mode_cluster: '切到集群模式',
  mode_ai: '切到 AI 优先',
  plans_view: '查看套餐',
  purchase_start: '发起购买',
  purchase_paid: '支付成功',
  referral_view: '查看邀请',
  invite_share: '分享邀请',
  devices_view: '查看多设备',
  apps_view: '打开应用页',
  app_rec_open: '打开推荐应用',
  ad_click: '点击广告',
  ad_popup_shown: '开屏弹窗展示',
  update_prompt: '提示更新',
  update_open: '去下载更新',
  settings_view: '打开设置',
  cache_clear: '清除缓存',
  language_change: '切换语言',
  appearance_change: '切换外观',
  crash: '崩溃 / 错误',
}

const crashKind: Record<string, string> = { dart: 'Dart 异常', flutter: '界面错误', native: '原生崩溃', vpn: 'VPN 核心' }

export default function AnalyticsPage() {
  const [days, setDays] = useState(14)
  const { data: a, loading, reload } = useAsync(() => api.analytics.get(days), [days])
  const series = (k: keyof Omit<DayStat, 'day'>) => (a?.daily ?? []).map((p) => ({ label: p.day.slice(5), value: p[k] }))
  return (
    <div className="page">
      <div className="toolbar">
        <Segmented value={days} onChange={(v) => setDays(v as number)} options={[{ label: '近 7 天', value: 7 }, { label: '近 14 天', value: 14 }, { label: '近 30 天', value: 30 }]} />
        <span className="muted">UV = 当天打开过 App 的去重用户（未登录时按设备计）</span>
        <div className="spacer" />
        <Button icon={<ReloadOutlined />} loading={loading} onClick={() => reload()}>刷新</Button>
      </div>
      {a && (
        <>
          <div className="stat-grid" style={{ marginBottom: 16 }}>
            <StatCard label="今日 UV" value={a.today_uv} sub={`打开 ${a.daily.at(-1)?.opens ?? 0} 次`} icon={<EyeOutlined />} color={colors.brand} />
            <StatCard label="近 7 天 UV" value={a.uv_7} icon={<TeamOutlined />} color={colors.sky} />
            <StatCard label="近 30 天 UV" value={a.uv_30} icon={<LineChartOutlined />} color={colors.violet} />
            <StatCard label="今日连接成功" value={a.daily.at(-1)?.connects ?? 0} icon={<ThunderboltOutlined />} color={colors.success} />
            <StatCard label="7 天崩溃" value={a.crash_reports_7} sub={`${a.crash_groups_7} 类未处理`} icon={<BugOutlined />} color={a.crash_groups_7 ? colors.danger : colors.muted} />
          </div>
          <Row gutter={[16, 16]}>
            <Col xs={24} md={12}><PageCard title="每日 UV"><BarChart points={series('uv')} color={colors.brand} /></PageCard></Col>
            <Col xs={24} md={12}><PageCard title="每日新增用户"><BarChart points={series('new_users')} color={colors.sky} /></PageCard></Col>
            <Col xs={24} md={12}><PageCard title="每日连接成功次数"><BarChart points={series('connects')} color={colors.success} /></PageCard></Col>
            <Col xs={24} md={12}><PageCard title="每日支付成功"><BarChart points={series('purchases')} color={colors.warning} /></PageCard></Col>
            <Col xs={24} lg={14}>
              <PageCard title="用户操作（近 7 天）">
                <Table
                  rowKey="event"
                  size="small"
                  pagination={{ pageSize: 15, hideOnSinglePage: true }}
                  dataSource={a.events}
                  locale={{ emptyText: '暂无数据（需要 App 1.2.2 及以上）' }}
                  columns={[
                    { title: '操作', render: (_, e) => <>{eventLabel[e.event] ?? e.event} <span className="muted mono" style={{ fontSize: 12 }}>{e.event}</span></> },
                    { title: '次数', dataIndex: 'count', width: 100, sorter: (x, y) => x.count - y.count },
                    { title: '人数', dataIndex: 'users', width: 100, sorter: (x, y) => x.users - y.users },
                  ]}
                />
              </PageCard>
            </Col>
            <Col xs={24} lg={10}>
              <PageCard title="平台 / 版本（近 7 天 UV）">
                <Space direction="vertical" style={{ width: '100%' }}>
                  {[...a.platforms, ...a.versions.map((v) => ({ ...v, key: 'v' + (v.key || '?') }))].map((b) => (
                    <div key={b.key} style={{ display: 'flex', justifyContent: 'space-between' }}>
                      <span>{b.key === 'android' ? 'Android' : b.key === 'ios' ? 'iPhone' : b.key || '未知'}</span>
                      <b>{b.users}</b>
                    </div>
                  ))}
                </Space>
              </PageCard>
            </Col>
          </Row>
        </>
      )}
      <Crashes />
    </div>
  )
}

function Crashes() {
  const { message } = App.useApp()
  const [all, setAll] = useState(false)
  const { data, loading, reload } = useAsync(() => api.analytics.crashes(all), [all])
  const [open, setOpen] = useState<Crash>()
  const { run } = useAction()
  const show = async (c: Crash) => {
    try {
      setOpen(await api.analytics.crash(c.id))
    } catch (e) {
      message.error(errMsg(e))
    }
  }
  return (
    <PageCard style={{ marginTop: 16 }} title="崩溃与错误日志"
      extra={<Space><span className="muted">显示已处理</span><Switch size="small" checked={all} onChange={setAll} /><Button size="small" icon={<ReloadOutlined />} onClick={() => reload()} /></Space>}>
      <Table<Crash>
        rowKey="id"
        size="small"
        loading={loading}
        dataSource={data?.items ?? []}
        pagination={{ pageSize: 20, hideOnSinglePage: true }}
        locale={{ emptyText: '没有崩溃记录' }}
        onRow={(c) => ({ onClick: () => show(c), style: { cursor: 'pointer' } })}
        columns={[
          { title: '类型', width: 100, render: (_, c) => <Tag bordered={false} color={c.kind === 'native' || c.kind === 'vpn' ? 'red' : 'orange'}>{crashKind[c.kind] ?? c.kind}</Tag> },
          { title: '错误', render: (_, c) => <Typography.Text ellipsis style={{ maxWidth: 520 }}>{c.message}</Typography.Text> },
          { title: '版本', width: 90, dataIndex: 'app_version' },
          { title: '次数 / 人数', width: 100, render: (_, c) => `${c.count} / ${c.users}` },
          { title: '最近', width: 150, render: (_, c) => dateTime(c.last_at) },
          { title: '', width: 90, render: (_, c) => (c.resolved ? <Tag>已处理</Tag> : null) },
        ]}
      />
      <Drawer open={!!open} onClose={() => setOpen(undefined)} size={760} title={open && (crashKind[open.kind] ?? open.kind)}
        extra={open && (
          <Button onClick={async () => { if (await run(() => api.analytics.resolve(open.id, !open.resolved), open.resolved ? '已重新打开' : '已标记处理')) { setOpen(undefined); reload(true) } }}>
            {open.resolved ? '重新打开' : '标记已处理'}
          </Button>
        )}>
        {open && (
          <>
            <p><b>{open.message}</b></p>
            <p className="muted">
              版本 {open.app_version} · {open.platform} · {open.device} · {open.os_version}<br />
              {open.count} 次 / {open.users} 人 · 首次 {dateTime(open.first_at)} · 最近 {dateTime(open.last_at)}
              {open.user_id > 0 && <> · 最近用户 VP-{String(open.user_id).padStart(5, '0')}</>}
            </p>
            <pre className="md-code" style={{ background: '#15171c', color: '#e8e6e0', padding: 12, borderRadius: 10, whiteSpace: 'pre-wrap', fontSize: 12 }}>{open.stack || '（无堆栈）'}</pre>
          </>
        )}
      </Drawer>
    </PageCard>
  )
}
