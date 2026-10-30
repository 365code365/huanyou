'use client'
import { CrownOutlined, DownloadOutlined, MoneyCollectOutlined, ReloadOutlined, RetweetOutlined, RiseOutlined, SettingOutlined, TeamOutlined } from '@ant-design/icons'
import { App, Button, Card, Col, Empty, Form, InputNumber, Modal, Row, Segmented, Space, Spin, Table, Tag, Tooltip } from 'antd'
import { useCallback, useEffect, useState } from 'react'
import { PageCard } from '@/components/PageCard'
import { StatCard } from '@/components/StatCard'
import { useAction, useAsync } from '@/hooks/useAsync'
import { api, type SegmentConfig, type SegmentStat, type SegmentUser } from '@/lib/api'
import { bytes, date, dateTime, errMsg, yuan } from '@/lib/format'
import { segmentMeta } from '@/lib/segments'
import { colors } from '@/lib/theme'

const pct = (x: number) => `${(x * 100).toFixed(x > 0 && x < 0.1 ? 1 : 0)}%`

function SegmentCard({ s, active, onClick }: { s: SegmentStat; active: boolean; onClick: () => void }) {
  return (
    <Card
      size="small"
      hoverable
      onClick={onClick}
      style={{ height: '100%', borderColor: active ? '#15171c' : undefined, boxShadow: active ? '0 0 0 1px #15171c' : undefined }}
      styles={{ body: { padding: 14 } }}
    >
      <Space style={{ justifyContent: 'space-between', width: '100%' }}>
        <Tag color={s.color} bordered={false} style={{ fontWeight: 600 }}>{s.label}</Tag>
        <span className="muted">{pct(s.share)}</span>
      </Space>
      <div style={{ fontSize: 24, fontWeight: 700, margin: '6px 0 2px' }}>{s.users}</div>
      <div className="muted" style={{ fontSize: 12, lineHeight: 1.7 }}>
        {s.revenue_cents > 0 && <>贡献 {yuan(s.revenue_cents)} · 客单 {yuan(s.avg_ltv_cents)}<br /></>}
        近 7 天活跃 {pct(s.active_share)} · 月均流量 {bytes(s.avg_bytes_30)}
      </div>
    </Card>
  )
}

function Members({ seg }: { seg: SegmentStat }) {
  const { message } = App.useApp()
  const [sort, setSort] = useState('ltv')
  const [pager, setPager] = useState({ page: 1, size: 20 })
  const [data, setData] = useState<{ items: SegmentUser[]; total: number }>({ items: [], total: 0 })
  const [loading, setLoading] = useState(false)
  const load = useCallback(async () => {
    setLoading(true)
    try {
      setData(await api.segments.users(seg.key, { ...pager, sort }))
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setLoading(false)
    }
  }, [seg.key, pager, sort, message])
  useEffect(() => {
    load()
  }, [load])
  useEffect(() => setPager((p) => ({ ...p, page: 1 })), [seg.key])
  const [exporting, setExporting] = useState(false)
  const exportCsv = async () => {
    setExporting(true)
    try {
      await api.segments.exportCsv(seg.key)
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setExporting(false)
    }
  }
  return (
    <PageCard
      style={{ marginTop: 16 }}
      title={<Space><Tag color={seg.color} bordered={false}>{seg.label}</Tag><span>{seg.users} 人</span></Space>}
      extra={<Button icon={<DownloadOutlined />} loading={exporting} onClick={exportCsv}>导出 CSV</Button>}
    >
      <div className="muted" style={{ marginBottom: 6 }}>规则：{seg.desc}</div>
      <div style={{ marginBottom: 12 }}><b>运营建议：</b>{seg.advice}</div>
      <div className="toolbar">
        <Segmented
          value={sort}
          onChange={(v) => setSort(v as string)}
          options={[{ label: '按消费', value: 'ltv' }, { label: '按流量', value: 'traffic' }, { label: '最近活跃', value: 'recent' }, { label: '最新注册', value: 'new' }]}
        />
      </div>
      <Table<SegmentUser>
        rowKey="id"
        size="small"
        loading={loading}
        dataSource={data.items}
        scroll={{ x: 980 }}
        pagination={{ current: pager.page, pageSize: pager.size, total: data.total, showSizeChanger: true, onChange: (page, size) => setPager({ page, size }), showTotal: (t) => `共 ${t} 人` }}
        columns={[
          { title: '充值 ID', width: 110, render: (_, u) => <span className="mono">{u.recharge_code}</span> },
          {
            title: '标签',
            render: (_, u) => (
              <Space size={[2, 2]} wrap>
                {[u.tier, ...u.tags].map((k) => <Tag key={k} bordered={false} color={segmentMeta[k]?.color}>{segmentMeta[k]?.label ?? k}</Tag>)}
              </Space>
            ),
          },
          { title: '累计消费', width: 100, render: (_, u) => (u.ltv_cents ? yuan(u.ltv_cents) : <span className="muted">—</span>) },
          { title: '付费次数', width: 80, dataIndex: 'paid_orders' },
          { title: '当前套餐', width: 140, render: (_, u) => (u.plan_id ? <>{u.plan_id}<div className="muted">{date(u.expires_at)} 到期</div></> : <span className="muted">无</span>) },
          { title: '近 30 天', width: 120, render: (_, u) => <>{bytes(u.bytes_30)}<div className="muted">活跃 {u.active_days_30} 天</div></> },
          { title: '最近活跃', width: 100, render: (_, u) => u.last_active_day || <span className="muted">90 天内无</span> },
          { title: '邀请', width: 60, dataIndex: 'invites' },
          { title: '注册', width: 100, render: (_, u) => date(u.created_at) },
        ]}
      />
    </PageCard>
  )
}

function ConfigModal({ open, value, onClose, onSaved }: { open: boolean; value?: SegmentConfig; onClose: () => void; onSaved: () => void }) {
  const [form] = Form.useForm()
  const { busy, run } = useAction()
  useEffect(() => {
    if (open && value) {
      form.setFieldsValue({ ...value, high_value_cents: value.high_value_cents / 100, low_value_cents: value.low_value_cents / 100 })
    }
  }, [open, value, form])
  const save = async () => {
    const v = await form.validateFields()
    const body: SegmentConfig = { ...v, high_value_cents: Math.round(v.high_value_cents * 100), low_value_cents: Math.round(v.low_value_cents * 100) }
    if (await run(() => api.segments.saveConfig(body), '已保存，分群已重新计算')) {
      onSaved()
      onClose()
    }
  }
  return (
    <Modal open={open} title="分群规则" onCancel={onClose} onOk={save} confirmLoading={busy} okText="保存" destroyOnHidden={false}>
      <Form form={form} layout="vertical">
        <Row gutter={16}>
          <Col span={12}>
            <Form.Item name="high_value_cents" label="高价门槛（累计消费）" rules={[{ required: true }]}>
              <InputNumber min={1} addonBefore="¥" style={{ width: '100%' }} />
            </Form.Item>
          </Col>
          <Col span={12}>
            <Form.Item name="low_value_cents" label="低价门槛（累计消费低于）" rules={[{ required: true }]}>
              <InputNumber min={1} addonBefore="¥" style={{ width: '100%' }} />
            </Form.Item>
          </Col>
          <Col span={12}>
            <Form.Item name="loyal_orders" label="优质客群：付费次数 ≥" rules={[{ required: true }]}>
              <InputNumber min={1} max={100} style={{ width: '100%' }} />
            </Form.Item>
          </Col>
          <Col span={12}>
            <Form.Item name="active_days" label="活跃：近 14 天使用天数 ≥" rules={[{ required: true }]}>
              <InputNumber min={1} max={14} style={{ width: '100%' }} />
            </Form.Item>
          </Col>
          <Col span={12}>
            <Form.Item name="promoter_invites" label="推广达人：邀请人数 ≥" rules={[{ required: true }]}>
              <InputNumber min={1} max={10000} style={{ width: '100%' }} />
            </Form.Item>
          </Col>
          <Col span={12}>
            <Form.Item name="churn_days" label="已流失：到期未续超过" rules={[{ required: true }]}>
              <InputNumber min={1} max={365} addonAfter="天" style={{ width: '100%' }} />
            </Form.Item>
          </Col>
        </Row>
      </Form>
    </Modal>
  )
}

export default function SegmentsPage() {
  const [refreshing, setRefreshing] = useState(false)
  const { data, loading, reload, setData } = useAsync(() => api.segments.report())
  const [selected, setSelected] = useState<string>()
  const [cfgOpen, setCfgOpen] = useState(false)
  const refresh = async () => {
    setRefreshing(true)
    try {
      setData(await api.segments.report(true))
    } finally {
      setRefreshing(false)
    }
  }
  const all = [...(data?.tiers ?? []), ...(data?.tags ?? [])]
  const sel = all.find((s) => s.key === selected)
  const t = data?.totals

  return (
    <div className="page">
      <PageCard>
        <div className="toolbar">
          <span className="muted">
            每位用户有一个价值分层（按累计消费）和若干行为标签。数据每 3 分钟更新{data && `，当前为 ${dateTime(data.generated_at)}`}。点卡片查看名单并导出。
          </span>
          <div className="spacer" />
          <Button icon={<SettingOutlined />} onClick={() => setCfgOpen(true)}>分群规则</Button>
          <Button icon={<ReloadOutlined />} loading={refreshing} onClick={refresh}>重新计算</Button>
        </div>
        <Spin spinning={loading && !data}>
          {t ? (
            <div className="stat-grid">
              <StatCard label="付费转化率" value={pct(t.conversion)} sub={`${t.paying}/${t.accounts} 付过费`} icon={<RiseOutlined />} color={colors.success} />
              <StatCard label="复购率" value={pct(t.repurchase_rate)} sub="付费 ≥2 次" icon={<RetweetOutlined />} color={colors.sky} />
              <StatCard label="ARPPU" value={yuan(t.arppu_cents)} sub="人均累计消费" icon={<CrownOutlined />} color={colors.violet} />
              <StatCard label="近 30 天收入" value={yuan(t.revenue_30_cents)} sub={`累计 ${yuan(t.revenue_cents)}`} icon={<MoneyCollectOutlined />} color={colors.warning} />
              <StatCard label="付费有效" value={t.paid_active} sub={`7 天活跃 ${t.active_7}`} icon={<TeamOutlined />} color={colors.brand} />
            </div>
          ) : !loading && <Empty />}
        </Spin>
      </PageCard>

      {data && (
        <>
          <PageCard style={{ marginTop: 16 }} title="价值分层" extra={<span className="muted">按累计消费，互斥</span>}>
            <Row gutter={[12, 12]}>
              {data.tiers.map((s) => (
                <Col key={s.key} xs={12} md={8} xl={24 / 5}>
                  <SegmentCard s={s} active={selected === s.key} onClick={() => setSelected(s.key)} />
                </Col>
              ))}
            </Row>
          </PageCard>
          <PageCard style={{ marginTop: 16 }} title="行为标签" extra={<span className="muted">可同时属于多个</span>}>
            <Row gutter={[12, 12]}>
              {data.tags.map((s) => (
                <Col key={s.key} xs={12} md={8} xl={6}>
                  <Tooltip title={s.desc}>
                    <div style={{ height: '100%' }}>
                      <SegmentCard s={s} active={selected === s.key} onClick={() => setSelected(s.key)} />
                    </div>
                  </Tooltip>
                </Col>
              ))}
            </Row>
          </PageCard>
          {sel && <Members seg={sel} />}
          <PageCard style={{ marginTop: 16 }} title="套餐销售构成">
            <Table
              rowKey="plan_id"
              size="small"
              pagination={false}
              dataSource={data.plan_mix ?? []}
              locale={{ emptyText: '暂无付费订单' }}
              columns={[
                { title: '套餐', render: (_, p) => <>{p.name} <span className="muted mono">{p.plan_id}</span></> },
                { title: '订单数', dataIndex: 'orders', width: 100 },
                { title: '购买人数', dataIndex: 'buyers', width: 100 },
                { title: '收入', width: 120, render: (_, p) => yuan(p.revenue_cents) },
                {
                  title: '收入占比',
                  width: 120,
                  render: (_, p) => (data.totals.revenue_cents ? pct(p.revenue_cents / data.totals.revenue_cents) : '—'),
                },
                { title: '人均购买次数', width: 120, render: (_, p) => (p.buyers ? (p.orders / p.buyers).toFixed(2) : '—') },
              ]}
            />
          </PageCard>
        </>
      )}
      <ConfigModal open={cfgOpen} value={data?.config} onClose={() => setCfgOpen(false)} onSaved={() => reload(true)} />
    </div>
  )
}
