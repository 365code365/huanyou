'use client'
import { CloudServerOutlined, DownOutlined, PlusOutlined, SyncOutlined } from '@ant-design/icons'
import { Alert, App, Button, Collapse, Drawer, Dropdown, Form, Input, InputNumber, Modal, Select, Space, Spin, Switch, Table, Typography } from 'antd'
import { useEffect, useState } from 'react'
import { NodeHealth } from '@/components/NodeHealth'
import { PageCard } from '@/components/PageCard'
import { useAction } from '@/hooks/useAsync'
import { useCatalog } from '@/hooks/useCatalog'
import { api, type VpnNode } from '@/lib/api'
import { bytes, errMsg, flag } from '@/lib/format'
import { ProvisionModal } from './ProvisionModal'

const blank = (): VpnNode => ({
  id: '', name: '', region: 'US', address: '', token: '', reality_target: '', vision_port: 443, xhttp_path: '',
  disabled: false, weight: 1, sort: 0,
  reality: { public_key: '', short_id: '', server_name: 'www.apple.com', fingerprint: 'chrome' },
})
// Status is response-only; the target follows the SNI.
const toBody = ({ status: _s, ...n }: VpnNode) => ({ ...n, reality_target: n.reality.server_name + ':443' }) as VpnNode

export default function NodesPage() {
  const { message, modal } = App.useApp()
  const c = useCatalog(api.nodes, blank, '节点')
  const { busy: checking, run } = useAction()
  const region = Form.useWatch('region', c.form)
  const [install, setInstall] = useState<{ node: VpnNode; data?: { node_json: string; steps: string }; err?: string }>()
  const [provisionOpen, setProvisionOpen] = useState(false)

  // Manual form: fill region (and name, if empty) from the address.
  const geoFill = async () => {
    const addr = (c.form.getFieldValue('address') as string | undefined)?.trim()
    if (!addr || c.mode !== 'create') return
    try {
      const g = await api.nodes.geoip(addr)
      c.form.setFieldsValue({ region: g.country_code, ...(c.form.getFieldValue('name') ? {} : { name: g.suggested_name }) })
      message.info(`已识别地区：${flag(g.country_code)} ${g.country}${g.city ? ' · ' + g.city : ''}`)
    } catch {
      // keep whatever the operator typed
    }
  }

  // Refresh health every 30 s unless a form is open.
  useEffect(() => {
    const t = setInterval(() => !c.open && c.load(true), 30000)
    return () => clearInterval(t)
  }, [c])

  const showInstall = async (node: VpnNode) => {
    setInstall({ node })
    try {
      setInstall({ node, data: await api.nodes.install(node.id) })
    } catch (e) {
      setInstall({ node, err: errMsg(e) })
    }
  }

  const save = async () => {
    const saved = await c.save(toBody)
    if (saved && c.mode === 'create') showInstall(saved)
  }

  // Push the API server's current vpn-node binary to this node over SSH.
  const [upgrading, setUpgrading] = useState<VpnNode>()
  const [upForm] = Form.useForm()
  const [upBusy, setUpBusy] = useState(false)
  const [upLog, setUpLog] = useState('')
  const openUpgrade = (n: VpnNode) => {
    upForm.setFieldsValue({ user: 'root', port: 22, password: '' })
    setUpLog('')
    setUpgrading(n)
  }
  const doUpgrade = async () => {
    const v = await upForm.validateFields()
    setUpBusy(true)
    setUpLog('')
    try {
      const r = await api.nodes.upgrade(upgrading!.id, v)
      setUpLog(r.log)
      message.success('节点程序已升级并重启')
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setUpBusy(false)
    }
  }

  const rotateToken = (n: VpnNode) =>
    modal.confirm({
      title: '重置 Token',
      content: '旧 Token 立即失效，需要把新的 node.json 更新到节点服务器并重启 vpn-node。',
      okButtonProps: { danger: true },
      onOk: async () => {
        if (await run(() => api.nodes.rotateToken(n.id), 'Token 已重置')) showInstall(n)
      },
    })

  return (
    <div className="page">
      <PageCard>
        <div className="toolbar">
          <span className="muted">填服务器 IP + SSH 密码即可自动安装上线；已有节点可手动添加</span>
          <div className="spacer" />
          <Button
            icon={<SyncOutlined spin={checking} />}
            loading={checking}
            onClick={() => run(async () => c.setItems((await api.monitor.checkNow()).items), '检测完成')}
          >
            立即检测
          </Button>
          <Button icon={<PlusOutlined />} onClick={() => c.openCreate({ sort: (c.items.at(-1)?.sort ?? 0) + 10 })}>手动添加</Button>
          <Button type="primary" icon={<CloudServerOutlined />} onClick={() => setProvisionOpen(true)}>一键安装节点</Button>
        </div>
        <Table<VpnNode>
          rowKey="id"
          loading={c.loading}
          dataSource={c.items}
          pagination={false}
          scroll={{ x: 1100 }}
          columns={[
            {
              title: '节点',
              render: (_, n) => (
                <>
                  <div style={{ fontWeight: 500 }}>{flag(n.region)} {n.name}</div>
                  <div className="muted">{n.id} · {n.address}:{n.vision_port}</div>
                </>
              ),
            },
            { title: '状态（近 1 小时）', width: 280, render: (_, n) => <NodeHealth status={n.status} disabled={n.disabled} history /> },
            { title: '可用率', width: 80, render: (_, n) => (n.status ? Math.round(n.status.uptime * 100) + '%' : '—') },
            { title: '伪装 SNI', render: (_, n) => <span className="mono">{n.reality.server_name}</span> },
            { title: '累计流量', width: 100, render: (_, n) => bytes(n.status?.traffic_bytes) },
            { title: '启用', width: 70, render: (_, n) => <Switch checked={!n.disabled} onChange={(v) => c.patch(n, { disabled: !v }, toBody)} /> },
            {
              title: '操作',
              width: 200,
              fixed: 'right',
              render: (_, n) => (
                <Space size={0}>
                  <Button type="link" size="small" onClick={() => c.openEdit(n)}>编辑</Button>
                  <Button type="link" size="small" onClick={() => showInstall(n)}>部署配置</Button>
                  <Dropdown
                    trigger={['click']}
                    menu={{
                      items: [
                        { key: 'upgrade', label: '升级节点程序' },
                        { key: 'token', label: '重置 Token' },
                        { type: 'divider' },
                        { key: 'delete', label: '删除节点', danger: true },
                      ],
                      onClick: ({ key }) => (key === 'delete' ? c.remove(n) : key === 'upgrade' ? openUpgrade(n) : rotateToken(n)),
                    }}
                  >
                    <Button type="link" size="small">更多 <DownOutlined /></Button>
                  </Dropdown>
                </Space>
              ),
            },
          ]}
        />
      </PageCard>

      <Drawer
        open={c.open}
        onClose={c.close}
        size={540}
        title={c.mode === 'create' ? '新增节点' : '编辑节点'}
        extra={<Space><Button onClick={c.close}>取消</Button><Button type="primary" loading={c.saving} onClick={save}>保存</Button></Space>}
      >
        <Form form={c.form} layout="vertical">
          <Space size={16} style={{ display: 'flex' }}>
            <Form.Item name="id" label="ID" rules={[{ required: true, pattern: /^[a-z0-9][a-z0-9_-]{0,31}$/, message: '小写字母 / 数字 / - / _' }]}>
              <Input disabled={c.mode === 'edit'} placeholder="如 us1、jp2" />
            </Form.Item>
            <Form.Item name="region" label="地区代码" rules={[{ required: true, len: 2, message: '两位国家代码' }]} normalize={(v: string) => v?.toUpperCase()}>
              <Input maxLength={2} placeholder="US" style={{ width: 110 }} suffix={<span style={{ fontSize: 18 }}>{flag(region ?? '')}</span>} />
            </Form.Item>
          </Space>
          <Form.Item name="name" label="名称" rules={[{ required: true }]}><Input placeholder="如 美国 01" /></Form.Item>
          <div style={{ display: 'flex', gap: 16, alignItems: 'flex-start' }}>
            <Form.Item name="address" label="地址" rules={[{ required: true }]} style={{ flex: 1 }}>
              <Input placeholder="公网 IP 或域名（填写后自动识别地区）" onBlur={geoFill} />
            </Form.Item>
            <Form.Item name="vision_port" label="端口（建议 443）"><InputNumber min={1} max={65535} style={{ width: 130 }} /></Form.Item>
          </div>
          <Form.Item
            name={['reality', 'server_name']}
            label="伪装域名 SNI"
            rules={[{ required: true }]}
            extra="必须是境外、支持 TLS1.3 + H2、走经典 X25519 的真实网站。使用抗量子密钥交换的站点（多数 Cloudflare / Google 站）会导致连接失败。"
          >
            <Input placeholder="www.apple.com" />
          </Form.Item>
          <Space size={16}>
            <Form.Item name={['reality', 'fingerprint']} label="TLS 指纹">
              <Select style={{ width: 140 }} options={['chrome', 'firefox', 'safari', 'ios', 'edge'].map((v) => ({ value: v }))} />
            </Form.Item>
            <Form.Item name="sort" label="排序"><InputNumber /></Form.Item>
            <Form.Item name="weight" label="权重"><InputNumber min={1} /></Form.Item>
          </Space>
          {c.mode === 'create' ? (
            <Collapse
              ghost
              items={[
                {
                  key: 'import',
                  label: '导入已有节点（可选）',
                  children: (
                    <>
                      <Alert type="info" showIcon style={{ marginBottom: 12 }} message="留空则自动生成新密钥。导入时需与节点服务器 node.json 中的私钥配对。" />
                      <Form.Item name={['reality', 'public_key']} label="REALITY 公钥"><Input /></Form.Item>
                      <Form.Item name={['reality', 'short_id']} label="Short ID"><Input /></Form.Item>
                      <Form.Item name="xhttp_path" label="XHTTP 路径"><Input placeholder="/xxxx" /></Form.Item>
                    </>
                  ),
                },
              ]}
            />
          ) : (
            <>
              <Form.Item label="REALITY 公钥 / Short ID">
                <Typography.Text className="mono" copyable>{c.form.getFieldValue(['reality', 'public_key'])}</Typography.Text>
                <div className="mono">{c.form.getFieldValue(['reality', 'short_id'])}</div>
              </Form.Item>
              <Form.Item name="xhttp_path" label="XHTTP 路径"><Input /></Form.Item>
            </>
          )}
        </Form>
      </Drawer>

      <Modal
        open={!!upgrading}
        title={`升级节点程序 · ${upgrading?.name ?? ''}`}
        onCancel={() => !upBusy && setUpgrading(undefined)}
        onOk={doUpgrade}
        okText="开始升级"
        confirmLoading={upBusy}
        destroyOnHidden
      >
        <p className="muted">把后台当前版本的 vpn-node 上传到 {upgrading?.address} 并重启，配置保持不变（连接会中断几秒）。</p>
        <Form form={upForm} layout="vertical">
          <Space size={12}>
            <Form.Item name="user" label="SSH 用户"><Input style={{ width: 120 }} /></Form.Item>
            <Form.Item name="port" label="SSH 端口"><InputNumber min={1} max={65535} style={{ width: 110 }} /></Form.Item>
          </Space>
          <Form.Item name="password" label="SSH 密码" rules={[{ required: true, message: '请填写 SSH 密码' }]}>
            <Input.Password autoComplete="new-password" />
          </Form.Item>
        </Form>
        {upLog && <pre className="provision-log" style={{ height: 160 }}>{upLog}</pre>}
      </Modal>

      <ProvisionModal open={provisionOpen} onClose={() => setProvisionOpen(false)} onDone={() => c.load(true)} />

      <Modal open={!!install} onCancel={() => setInstall(undefined)} footer={null} width={700} title={`部署配置 · ${install?.node.name ?? ''}`}>
        {install?.err ? (
          <Alert type="warning" showIcon message={install.err} />
        ) : !install?.data ? (
          <Spin style={{ display: 'block', margin: 40 }} />
        ) : (
          <>
            <p className="muted">保存为节点服务器的 <code>/etc/vpn/node.json</code>（含私钥，注意保密）：</p>
            <div className="code-block">
              <pre>{install.data.node_json}</pre>
              <Button
                size="small"
                style={{ position: 'absolute', top: 8, right: 8 }}
                onClick={() => navigator.clipboard.writeText(install.data!.node_json).then(() => message.success('已复制'))}
              >
                复制
              </Button>
            </div>
            <p className="muted">安装步骤：</p>
            <div className="code-block"><pre>{install.data.steps}</pre></div>
          </>
        )}
      </Modal>
    </div>
  )
}
