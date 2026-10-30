import { Card, type CardProps } from 'antd'

/** Standard bordered card used for page sections. */
export function PageCard(props: CardProps) {
  return <Card variant="borderless" className="page-card" {...props} />
}
