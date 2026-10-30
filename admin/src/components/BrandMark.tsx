/** The product mark (same geometry as the app icon): open ring + green dot on an ink tile. */
export function BrandMark({ size = 30, inverted = false }: { size?: number; inverted?: boolean }) {
  // Ring: center 50,50 r=25, gap of 0.42π centered at -45°.
  const r = 25
  const gapC = -Math.PI / 4
  const half = (Math.PI * 0.42) / 2
  const p = (a: number) => [50 + r * Math.cos(a), 50 + r * Math.sin(a)].map((v) => v.toFixed(2)).join(' ')
  const start = p(gapC + half)
  const end = p(gapC - half)
  const dot = [50 + r * Math.cos(gapC), 50 + r * Math.sin(gapC)]
  return (
    <svg width={size} height={size} viewBox="0 0 100 100" style={{ flex: 'none', display: 'block' }} aria-hidden>
      <rect width="100" height="100" rx="27" fill={inverted ? '#fff' : '#15171c'} />
      <path d={`M ${start} A ${r} ${r} 0 1 1 ${end}`} fill="none" stroke={inverted ? '#15171c' : '#fff'} strokeWidth="9.5" strokeLinecap="round" />
      <circle cx={dot[0]} cy={dot[1]} r="7.5" fill="#12a150" />
    </svg>
  )
}
