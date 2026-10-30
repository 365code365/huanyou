'use client'
import { ReloadOutlined } from '@ant-design/icons'
import { Button, Col, Form, Input, InputNumber, Row, Select, Space, Spin, Switch, Table, Tag } from 'antd'
import { useEffect } from 'react'
import { PageCard } from '@/components/PageCard'
import { useAction, useAsync } from '@/hooks/useAsync'
import { api, type MonitorEvent, type MonitorSettings, type Settings } from '@/lib/api'
import { dateTime } from '@/lib/format'

const levelTag = { error: <Tag color="error">严重</Tag>, warning: <Tag color="warning">警告</Tag>, info: <Tag color="success">恢复</Tag> }

export default function MonitorPage() {
  const settings = useAsync(api.settings.get)
  const events = useAsync(api.monitor.events)
  const [form] = Form.useForm<MonitorSettings>()
  const { busy, run } = useAction()
  const webhook = Form.useWatch('alert_webhook', form)

  useEffect(() => {
    if (settings.data) form.setFieldsValue(settings.data.settings.monitor)
  }, [settings.data, form])

  const save = async (quietOk = false) => {
    const monitor = await form.validateFields()
    const current = settings.data!.settings
    const body: Settings = { ...current, monitor: { ...current.monitor, ...monitor } }
    return run(async () => {
      const saved = await api.settings.save(body)
      settings.setData({ ...settings.data!, settings: saved })
    }, quietOk ? undefined : '已保存，立即生效')
  }

  return (
    <div className="page">
      <Row gutter={16}>
        <Col xs={24} lg={9}>
          <PageCard title="监控配置">
            {!settings.data ? (
              <Spin />
            ) : (
              <Form form={form} layout="vertical">
                <Form.Item name="enabled" label="启用监控" valuePropName="checked"><Switch /></Form.Item>
                <Space size={16} wrap>
                  <Form.Item name="interval_seconds" label="检测间隔"><InputNumber min={10} suffix="秒" /></Form.Item>
                  <Form.Item name="timeout_seconds" label="超时"><InputNumber min={1} max={60} suffix="秒" /></Form.Item>
                  <Form.Item name="fail_threshold" label="离线阈值" extra="连续失败次数"><InputNumber min={1} suffix="次" /></Form.Item>
                </Space>
                <Form.Item name="alert_format" label="预警格式">
                  <Select
                    options={[
                      { value: 'generic', label: '通用 / Slack（{"text"}）' },
                      { value: 'feishu', label: '飞书机器人' },
                      { value: 'dingtalk', label: '钉钉机器人' },
                      { value: 'wecom', label: '企业微信机器人' },
                    ]}
                  />
                </Form.Item>
                <Form.Item name="alert_webhook" label="Webhook" extra="节点离线 / 恢复 / 节点代理失联时推送" rules={[{ type: 'url', message: '请输入 http(s) 地址' }]}>
                  <Input placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/..." />
                </Form.Item>
                <Space>
                  <Button type="primary" loading={busy} onClick={() => save()}>保存</Button>
                  <Button
                    disabled={!webhook}
                    loading={busy}
                    onClick={async () => {
                      if (await save(true)) await run(api.monitor.testAlert, '测试消息已发送')
                    }}
                  >
                    发送测试消息
                  </Button>
                </Space>
              </Form>
            )}
          </PageCard>
        </Col>
        <Col xs={24} lg={15}>
          <PageCard title="预警记录" extra={<Button type="text" icon={<ReloadOutlined />} onClick={() => events.reload()}>刷新</Button>}>
            <Table<MonitorEvent>
              rowKey={(e) => e.at + e.node_id + e.text}
              size="small"
              loading={events.loading}
              dataSource={events.data?.items}
              pagination={{ pageSize: 20, hideOnSinglePage: true }}
              locale={{ emptyText: '暂无预警（服务重启后清空）' }}
              columns={[
                { title: '时间', width: 150, render: (_, e) => dateTime(e.at) },
                { title: '级别', width: 80, render: (_, e) => levelTag[e.level] },
                { title: '节点', dataIndex: 'node_id', width: 90 },
                { title: '内容', dataIndex: 'text' },
              ]}
            />
          </PageCard>
        </Col>
      </Row>
    </div>
  )
}
