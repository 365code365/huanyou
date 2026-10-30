import type { ReactNode } from 'react'

export function StatCard({ label, value, icon, color, sub }: { label: string; value: ReactNode; icon: ReactNode; color: string; sub?: string }) {
  return (
    <div style={{ background: '#fff', border: '1px solid #e8ebf3', borderRadius: 12, padding: 18, display: 'flex', gap: 14, alignItems: 'center' }}>
      <div style={{ width: 46, height: 46, borderRadius: 12, display: 'grid', placeItems: 'center', flex: 'none', fontSize: 22, background: color + '1a', color }}>{icon}</div>
      <div style={{ minWidth: 0 }}>
        <div style={{ fontSize: 24, fontWeight: 700, lineHeight: 1.2 }}>{value}</div>
        <div style={{ color: '#8a94a6', fontSize: 13, marginTop: 2 }}>
          {label}
          {sub && <span style={{ color: '#b0b8c6' }}> · {sub}</span>}
        </div>
      </div>
    </div>
  )
}
