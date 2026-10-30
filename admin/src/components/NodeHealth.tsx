'use client'
import { Space, Tag, Tooltip } from 'antd'
import type { NodeStatus } from '@/lib/api'
import { ago, dateTime } from '@/lib/format'

/** Online / agent tags, optionally with the last-hour probe strip. */
export function NodeHealth({ status, disabled, history }: { status?: NodeStatus; disabled?: boolean; history?: boolean }) {
  if (disabled) return <Tag>已停用</Tag>
  const agentSeen = status?.agent_seen && !status.agent_seen.startsWith('0001')
  return (
    <div>
      <Space size={4} wrap>
        {status?.online ? <Tag color="success">在线 {status.latency_ms}ms</Tag> : <Tag color="error">离线</Tag>}
        <Tooltip title={agentSeen ? `节点代理最后同步：${ago(status!.agent_seen)}，下发用户 ${status!.users} 个` : '节点代理尚未连接 API（检查节点 api_url / token）'}>
          <Tag color={status?.agent_online ? 'green' : 'orange'} bordered={false}>
            代理{status?.agent_online ? '正常' : '未同步'}
          </Tag>
        </Tooltip>
      </Space>
      {history && !!status?.history?.length && (
        <div style={{ display: 'flex', gap: 2, marginTop: 6 }}>
          {status.history.map((p, i) => (
            <Tooltip key={i} title={`${dateTime(p.at)} ${p.ok ? p.latency_ms + 'ms' : '失败'}`}>
              <i style={{ flex: 1, maxWidth: 6, height: 16, borderRadius: 2, background: p.ok ? '#22c55e' : '#ef4444' }} />
            </Tooltip>
          ))}
        </div>
      )}
    </div>
  )
}
