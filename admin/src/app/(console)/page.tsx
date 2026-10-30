'use client'
import { ApiOutlined, CrownOutlined, LineChartOutlined, MoneyCollectOutlined, UserOutlined } from '@ant-design/icons'
import { Button, Col, Empty, Row, Spin, Table, Timeline } from 'antd'
import Link from 'next/link'
import { useEffect } from 'react'
import { BarChart } from '@/components/BarChart'
import { NodeHealth } from '@/components/NodeHealth'
import { PageCard } from '@/components/PageCard'
import { StatCard } from '@/components/StatCard'
import { useAsync } from '@/hooks/useAsync'
import { api, type TrendPoint, type VpnNode } from '@/lib/api'
import { bytes, dateTime, flag, yuan } from '@/lib/format'
import { colors } from '@/lib/theme'

export default function DashboardPage() {
  const { data: d, loading, reload } = useAsync(api.dashboard)

  useEffect(() => {
    const t = setInterval(() => reload(true), 30000)
    return () => clearInterval(t)
  }, [reload])

  if (!d) return <Spin spinning={loading} fullscreen={false} style={{ display: 'block', margin: 80 }} />

  const series = (k: keyof Omit<TrendPoint, 'day'>) => d.trend.map((p) => ({ label: p.day.slice(5), value: p[k] }))
  const s = d.stats

  return (
    <div className="page">
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(190px, 1fr))', gap: 16, marginBottom: 16 }}>
        <StatCard label="用户总数" value={s.users} sub={`今日 +${s.new_today}`} icon={<UserOutlined />} color={colors.brand} />
        <StatCard label="付费有效" value={s.paid_active} icon={<CrownOutlined />} color={colors.success} />
        <StatCard label="今日收入" value={yuan(s.revenue_today_cents)} sub={`累计 ${yuan(s.revenue_cents)}`} icon={<MoneyCollectOutlined />} color={colors.warning} />
        <StatCard label="今日流量" value={bytes(s.traffic_today_bytes)} sub={`${s.active_today} 人使用`} icon={<LineChartOutlined />} color={colors.violet} />
        <StatCard
          label="节点在线"
          value={`${d.nodes_online} / ${d.nodes.filter((n) => !n.disabled).length}`}
          icon={<ApiOutlined />}
          color={d.nodes_online < d.nodes.filter((n) => !n.disabled).length ? colors.danger : colors.sky}
        />
      </div>

      <Row gutter={[16, 16]}>
        <Col xs={24} md={8}>
          <PageCard title="近 14 天新增用户">
            <BarChart points={series('new_users')} color={colors.brand} />
          </PageCard>
        </Col>
        <Col xs={24} md={8}>
          <PageCard title="近 14 天收入">
            <BarChart points={series('revenue_cents')} color={colors.warning} format={yuan} />
          </PageCard>
        </Col>
        <Col xs={24} md={8}>
          <PageCard title="近 14 天流量">
            <BarChart points={series('traffic_bytes')} color={colors.violet} format={bytes} />
          </PageCard>
        </Col>

        <Col xs={24} md={15}>
          <PageCard title="节点状态" extra={<Link href="/nodes"><Button type="link">管理节点</Button></Link>}>
            <Table<VpnNode>
              rowKey="id"
              size="small"
              pagination={false}
              dataSource={d.nodes}
              columns={[
                { title: '节点', render: (_, n) => <>{flag(n.region)} {n.name} <span className="muted">{n.id}</span></> },
                { title: '地址', dataIndex: 'address' },
                { title: '状态', render: (_, n) => <NodeHealth status={n.status} disabled={n.disabled} /> },
                { title: '可用率', width: 90, render: (_, n) => (n.status ? Math.round(n.status.uptime * 100) + '%' : '—') },
              ]}
            />
          </PageCard>
        </Col>
        <Col xs={24} md={9}>
          <PageCard title="最近预警" extra={<Link href="/monitor"><Button type="link">全部</Button></Link>}>
            {d.events?.length ? (
              <Timeline
                items={d.events.map((e) => ({
                  color: e.level === 'error' ? 'red' : e.level === 'warning' ? 'orange' : 'green',
                  content: (
                    <>
                      <div>{e.text}</div>
                      <div className="muted">{dateTime(e.at)}</div>
                    </>
                  ),
                }))}
              />
            ) : (
              <Empty description="暂无预警" image={Empty.PRESENTED_IMAGE_SIMPLE} />
            )}
          </PageCard>
        </Col>
      </Row>
    </div>
  )
}
