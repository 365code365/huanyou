'use client'
import { DownOutlined, UpOutlined } from '@ant-design/icons'
import { Alert, Button, type AlertProps } from 'antd'
import { useEffect, useState, type ReactNode } from 'react'

/**
 * A one-line alert whose details fold away: the headline stays visible, the
 * description opens with 「展开」. The choice is remembered per `id`, so an
 * operator who closed it once does not get the full block on every visit.
 */
export function FoldAlert({
  id,
  description,
  defaultOpen = false,
  ...rest
}: Omit<AlertProps, 'description' | 'action'> & { id: string; description: ReactNode; defaultOpen?: boolean }) {
  const key = 'fold_' + id
  const [open, setOpen] = useState(defaultOpen)
  useEffect(() => {
    const v = localStorage.getItem(key)
    if (v !== null) setOpen(v === '1')
  }, [key])
  const toggle = () => {
    setOpen(!open)
    localStorage.setItem(key, open ? '0' : '1')
  }
  return (
    <Alert
      {...rest}
      className={'fold-alert ' + (rest.className ?? '')}
      description={open ? description : undefined}
      action={
        <Button type="link" size="small" onClick={toggle} style={{ paddingInline: 4 }}>
          {open ? <>收起 <UpOutlined /></> : <>展开 <DownOutlined /></>}
        </Button>
      }
    />
  )
}
