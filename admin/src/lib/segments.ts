// Segment keys from the server (api/segments.go) with display label + tag color.
export const segmentMeta: Record<string, { label: string; color: string }> = {
  high: { label: '高价客群', color: 'gold' },
  mid: { label: '中价客群', color: 'blue' },
  low: { label: '低价客群', color: 'cyan' },
  free: { label: '未付费', color: 'default' },
  member: { label: '共享成员', color: 'purple' },
  loyal: { label: '优质客群', color: 'green' },
  potential: { label: '潜力客群', color: 'lime' },
  at_risk: { label: '流失风险', color: 'orange' },
  churned: { label: '已流失', color: 'red' },
  dormant: { label: '沉睡用户', color: 'default' },
  new: { label: '新用户', color: 'geekblue' },
  promoter: { label: '推广达人', color: 'magenta' },
  risky: { label: '风险账号', color: 'volcano' },
}

export const ipClassLabel: Record<string, { label: string; color: string }> = {
  cn: { label: '国内', color: 'red' },
  abroad: { label: '海外', color: 'blue' },
  private: { label: '内网', color: 'default' },
}
