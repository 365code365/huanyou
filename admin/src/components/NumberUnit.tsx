'use client'
import { InputNumber, type InputNumberProps, Space } from 'antd'

/**
 * Number input with a unit ("天", "GB", "秒"…) shown as a separate addon block.
 *
 * Why not `<InputNumber suffix="天" />`: in Ant Design 6 the suffix and the
 * step arrows share the input's inner width; on focus / hover the arrows
 * appear and the narrow default width leaves no room, so the typed value
 * disappears. Here the field has a fixed width and the unit sits outside it.
 *
 * Works as a Form.Item child (receives id / value / onChange).
 */
export function NumberUnit({ unit, width = 120, style, ...rest }: InputNumberProps & { unit: string; width?: number }) {
  return (
    <Space.Compact>
      <InputNumber {...rest} style={{ width, ...style }} />
      <Space.Addon>{unit}</Space.Addon>
    </Space.Compact>
  )
}
