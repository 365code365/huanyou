'use client'
import { ApiOutlined, CheckCircleFilled, DatabaseOutlined, SwapOutlined, SyncOutlined } from '@ant-design/icons'
import { Alert, App, Button, Col, Descriptions, Form, Input, Progress, Row, Segmented, Space, Table, Tag } from 'antd'
import { useState } from 'react'
import { PageCard } from '@/components/PageCard'
import { useAsync } from '@/hooks/useAsync'
import { api, type DBDriver, type TableResult } from '@/lib/api'
import { dateTime, errMsg } from '@/lib/format'

const driverName: Record<DBDriver, string> = { sqlite: 'SQLite（本机文件）', mysql: 'MySQL', postgres: 'PostgreSQL' }
const tableName: Record<string, string> = {
  users: '用户', orders: '订单', traffic_daily: '每日流量', user_ips: 'IP 记录', link_codes: '设备令牌',
  risk_rules: '封禁规则', settings: '设置', releases: '版本', lang_packs: '语言包',
}
const placeholder: Record<DBDriver, string> = {
  sqlite: '',
  mysql: 'vpn:密码@tcp(10.0.0.5:3306)/vpn',
  postgres: 'postgres://vpn:密码@10.0.0.5:5432/vpn?sslmode=disable',
}

function ResultTable({ rows }: { rows: TableResult[] }) {
  return (
    <Table<TableResult>
      rowKey="table"
      size="small"
      pagination={false}
      dataSource={rows}
      columns={[
        { title: '表', render: (_, r) => <>{tableName[r.table] ?? r.table} <span className="muted mono">{r.table}</span></> },
        { title: '写入 / 更新', dataIndex: 'upserted', width: 110 },
        { title: '删除', dataIndex: 'deleted', width: 80 },
      ]}
    />
  )
}

export default function DatabasePage() {
  const { message, modal } = App.useApp()
  const { data: info, loading, reload } = useAsync(api.database.info)
  const [driver, setDriver] = useState<DBDriver>('mysql')
  const [dsn, setDsn] = useState('')
  const [testing, setTesting] = useState(false)
  const [tested, setTested] = useState<{ empty: boolean; counts: Record<string, number>; ms: number }>()
  const [syncing, setSyncing] = useState(false)
  const [progress, setProgress] = useState<{ table: string; rows: number }>()
  const [result, setResult] = useState<{ tables: TableResult[]; seconds: number; full: boolean }>()
  const [switching, setSwitching] = useState(false)

  const current = info?.driver
  const targetReady = driver === 'sqlite' || dsn.trim() !== ''
  const reset = () => {
    setTested(undefined)
    setResult(undefined)
  }

  const test = async () => {
    setTesting(true)
    try {
      const r = await api.database.test(driver, dsn.trim())
      setTested(r)
      message.success(`${r.message}（${r.ms} ms）`)
    } catch (e) {
      setTested(undefined)
      message.error(errMsg(e))
    } finally {
      setTesting(false)
    }
  }

  const sync = async (full: boolean) => {
    setSyncing(true)
    setResult(undefined)
    setProgress(undefined)
    let isFull = full
    try {
      await api.database.sync(driver, dsn.trim(), full, (e) => {
        if (e.type === 'start') isFull = e.full
        else if (e.type === 'progress') setProgress({ table: e.table, rows: e.rows })
        else if (e.type === 'done') setResult({ tables: e.result.tables, seconds: e.result.seconds, full: isFull })
        else if (e.type === 'error') throw new Error(e.message)
      })
      message.success('同步完成')
      reload(true)
    } catch (e) {
      message.error('同步失败：' + errMsg(e))
    } finally {
      setSyncing(false)
      setProgress(undefined)
    }
  }

  const switchTo = () =>
    modal.confirm({
      title: `切换到 ${driverName[driver]}`,
      width: 520,
      content: (
        <div>
          <p>将执行：</p>
          <ol style={{ paddingLeft: 18 }}>
            <li>把当前数据库完整同步到目标库（以当前库为准，目标库多出的数据会被删除）；</li>
            <li>保存数据库配置，服务自动重启（约 5 秒，App 会自动重连）；</li>
            <li>启动时再补一次切换过程中新写入的数据。</li>
          </ol>
          <p className="muted">原数据库保持不动，可随时用同样的方式切回。</p>
        </div>
      ),
      okText: '同步并切换',
      onOk: async () => {
        setSwitching(true)
        try {
          const r = await api.database.switchTo(driver, dsn.trim())
          message.success(r.message, 6)
          // Wait for the restart, then reload the info.
          for (let i = 0; i < 20; i++) {
            await new Promise((ok) => setTimeout(ok, 1500))
            try {
              const n = await api.database.info()
              if (n.driver === driver) break
            } catch {
              /* still restarting */
            }
          }
          reload()
          reset()
        } catch (e) {
          message.error(errMsg(e))
        } finally {
          setSwitching(false)
        }
      },
    })

  const marks = Object.entries(info?.marks ?? {})
  return (
    <div className="page">
      <Row gutter={16}>
        <Col xs={24} xl={10}>
          <PageCard title={<Space><DatabaseOutlined /> 当前数据库</Space>} loading={loading && !info}>
            {info && (
              <>
                <Descriptions size="small" column={1} bordered>
                  <Descriptions.Item label="类型">
                    <Tag color="blue" bordered={false}>{driverName[info.driver]}</Tag>
                    {info.ping_error ? <Tag color="error">连接异常</Tag> : <Tag color="success" bordered={false}>{info.ping_ms} ms</Tag>}
                  </Descriptions.Item>
                  <Descriptions.Item label="连接"><span className="mono" style={{ wordBreak: 'break-all' }}>{info.dsn}</span></Descriptions.Item>
                  <Descriptions.Item label="表结构版本">v{info.schema_version}</Descriptions.Item>
                </Descriptions>
                {info.ping_error && <Alert type="error" showIcon style={{ marginTop: 12 }} message={info.ping_error} />}
                {info.env_override && <Alert type="warning" showIcon style={{ marginTop: 12 }} message="数据库由环境变量 VPN_DB_DRIVER / VPN_DB_DSN 指定，后台不能切换" />}
                <Table
                  style={{ marginTop: 12 }}
                  size="small"
                  pagination={false}
                  rowKey="t"
                  dataSource={Object.entries(info.counts).map(([t, n]) => ({ t, n }))}
                  columns={[
                    { title: '表', render: (_, r) => <>{tableName[r.t] ?? r.t} <span className="muted mono">{r.t}</span></> },
                    { title: '行数', dataIndex: 'n', width: 100 },
                  ]}
                />
                {marks.length > 0 && (
                  <>
                    <h4 style={{ margin: '16px 0 8px' }}>同步记录</h4>
                    {marks.map(([target, m]) => (
                      <div key={target} className="muted" style={{ marginBottom: 6 }}>
                        <span className="mono">{target}</span>
                        <br />
                        {dateTime(new Date(m.at * 1000).toISOString())} · {m.full ? '全量' : '增量'} · {m.seconds.toFixed(1)} 秒
                      </div>
                    ))}
                  </>
                )}
              </>
            )}
          </PageCard>
        </Col>
        <Col xs={24} xl={14}>
          <PageCard title={<Space><SwapOutlined /> 同步 / 切换到其他数据库</Space>}>
            <Alert
              type="info"
              showIcon
              style={{ marginBottom: 16 }}
              message="数据量大、并发高时建议用 MySQL 8.0.19+ 或 PostgreSQL 12+。"
              description="目标库需先建好空库和账号（有建表权限）。同步可重复执行：首次全量，之后只同步变化的数据（按每张表的唯一业务键对齐，不依赖自增 ID）。切换前目标库不要被其他程序写入。"
            />
            <Form layout="vertical">
              <Form.Item label="目标数据库">
                <Segmented
                  value={driver}
                  onChange={(v) => {
                    setDriver(v as DBDriver)
                    reset()
                  }}
                  options={(['mysql', 'postgres', 'sqlite'] as DBDriver[]).map((d) => ({ label: driverName[d], value: d, disabled: d === current && d === 'sqlite' }))}
                />
              </Form.Item>
              {driver !== 'sqlite' ? (
                <Form.Item label="连接串" extra={driver === 'mysql' ? '格式：用户名:密码@tcp(主机:端口)/数据库名，需要 TLS 时加 ?tls=true' : '格式：postgres://用户名:密码@主机:端口/数据库名?sslmode=disable（云数据库用 sslmode=require）'}>
                  <Input.Password
                    visibilityToggle
                    className="mono"
                    value={dsn}
                    placeholder={placeholder[driver]}
                    onChange={(e) => {
                      setDsn(e.target.value)
                      reset()
                    }}
                  />
                </Form.Item>
              ) : (
                <Alert type="info" showIcon style={{ marginBottom: 16 }} message={`切回本机 SQLite 文件：${info?.sqlite_path ?? ''}`} />
              )}
              <Space wrap>
                <Button icon={<ApiOutlined />} loading={testing} disabled={!targetReady || syncing || switching} onClick={test}>测试连接</Button>
                <Button icon={<SyncOutlined />} loading={syncing} disabled={!targetReady || switching} onClick={() => sync(false)}>一键同步</Button>
                <Button disabled={!targetReady || syncing || switching} onClick={() => sync(true)}>全量重新同步</Button>
                <Button type="primary" danger icon={<SwapOutlined />} loading={switching} disabled={!targetReady || syncing || info?.env_override} onClick={switchTo}>
                  切换为主数据库
                </Button>
              </Space>
            </Form>
            {tested && (
              <Alert
                style={{ marginTop: 16 }}
                type={tested.empty ? 'success' : 'warning'}
                showIcon
                icon={<CheckCircleFilled />}
                message={tested.empty ? `连接成功（${tested.ms} ms），目标库为空` : `连接成功（${tested.ms} ms），目标库已有数据，同步会以当前库为准覆盖`}
              />
            )}
            {syncing && (
              <div style={{ marginTop: 16 }}>
                <Progress percent={99} status="active" showInfo={false} />
                <span className="muted">{progress ? `正在同步 ${tableName[progress.table] ?? progress.table}：${progress.rows} 行` : '准备中…'}</span>
              </div>
            )}
            {result && (
              <div style={{ marginTop: 16 }}>
                <Alert type="success" showIcon style={{ marginBottom: 8 }} message={`${result.full ? '全量' : '增量'}同步完成，用时 ${result.seconds.toFixed(2)} 秒`} />
                <ResultTable rows={result.tables} />
              </div>
            )}
          </PageCard>
        </Col>
      </Row>
    </div>
  )
}
