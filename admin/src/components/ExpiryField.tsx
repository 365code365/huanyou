'use client'
import { DatePicker, Form, Input, Space } from 'antd'
import dayjs from 'dayjs'

/** Server rental fields: expiry (to the minute, required) + vendor name / console URL. */
export function RentalFields({ required = true }: { required?: boolean }) {
  return (
    <>
      <Form.Item
        name="expires_at"
        label="服务器到期时间"
        rules={[{ required, message: '请选择到期时间' }]}
        extra="仅后台可见；7 天内到期会在概览和预警里提醒"
        getValueProps={(v?: string | null) => ({ value: v ? dayjs(v) : null })}
        normalize={(d?: dayjs.Dayjs | null) => (d ? d.second(0).millisecond(0).toISOString() : null)}
      >
        <DatePicker showTime={{ format: 'HH:mm' }} format="YYYY-MM-DD HH:mm" style={{ width: 220 }} />
      </Form.Item>
      <Space size={16} style={{ display: 'flex' }} align="start">
        <Form.Item name="vendor" label="厂商名称" style={{ width: 180 }}>
          <Input placeholder="如 搬瓦工、Vultr" maxLength={40} />
        </Form.Item>
        <Form.Item name="vendor_url" label="厂商地址（续费入口）" style={{ flex: 1, minWidth: 240 }} rules={[{ pattern: /^https?:\/\//, message: '以 http(s):// 开头' }]}>
          <Input placeholder="https://…/clientarea" />
        </Form.Item>
      </Space>
    </>
  )
}
