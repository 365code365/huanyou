'use client'
import { Select, type SelectProps } from 'antd'
import type { Plan } from '@/lib/api'
import { yuan } from '@/lib/format'

/** Plan dropdown showing price / duration. Works as a Form.Item child (value/onChange). */
export function PlanSelect({ plans, ...rest }: { plans: Plan[] } & Omit<SelectProps<string>, 'options'>) {
  return (
    <Select<string>
      style={{ width: '100%' }}
      {...rest}
      options={plans.map((p) => ({
        value: p.id,
        label: (
          <div style={{ display: 'flex', justifyContent: 'space-between', gap: 12 }}>
            <span>
              {p.name} <span className="muted">{p.id}</span>
            </span>
            <span className="muted">{p.free ? '免费' : `${yuan(p.price_cents)} / ${p.duration_days}天`}</span>
          </div>
        ),
      }))}
    />
  )
}
