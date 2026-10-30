'use client'
import { Tooltip } from 'antd'

/** Lightweight bar chart (no chart library needed for 14–30 bars). */
export function BarChart({
  points,
  color = '#15171c',
  format = String,
  height = 140,
}: {
  points: { label: string; value: number }[]
  color?: string
  format?: (v: number) => string
  height?: number
}) {
  const max = Math.max(1, ...points.map((p) => p.value))
  return (
    <div>
      <div style={{ height, display: 'flex', alignItems: 'flex-end', gap: 4 }}>
        {points.map((p, i) => (
          <Tooltip key={i} title={`${p.label}：${format(p.value)}`}>
            <div style={{ flex: 1, height: '100%', display: 'flex', alignItems: 'flex-end' }}>
              <div
                style={{
                  width: '100%',
                  height: p.value === 0 ? 2 : `${Math.max(4, (p.value / max) * 100)}%`,
                  background: p.value === 0 ? '#e5e7eb' : color,
                  borderRadius: '4px 4px 0 0',
                }}
              />
            </div>
          </Tooltip>
        ))}
      </div>
      <div style={{ display: 'flex', justifyContent: 'space-between', color: '#8a94a6', fontSize: 12, marginTop: 6 }}>
        <span>{points[0]?.label}</span>
        <span>{points[points.length - 1]?.label}</span>
      </div>
    </div>
  )
}
