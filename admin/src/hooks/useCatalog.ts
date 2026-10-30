'use client'
import { App, Form } from 'antd'
import { useCallback, useEffect, useRef, useState } from 'react'
import type { CatalogApi } from '@/lib/api'
import { errMsg } from '@/lib/format'

/**
 * List + create/edit form + delete + inline toggle for a catalog resource
 * (plans, nodes, apps). The page owns the table columns and form fields.
 *
 *   const c = useCatalog(api.plans, blankPlan, '套餐')
 *   c.openCreate(); c.openEdit(row); c.save(); c.remove(row); c.patch(row, { hidden: true })
 *   <Form form={c.form}> ... </Form>  inside <Drawer open={c.open} onClose={c.close}>
 */
export function useCatalog<T extends { id: string; name?: string }>(res: CatalogApi<T>, blank: () => T, label: string) {
  const { message, modal } = App.useApp()
  const [form] = Form.useForm<T>()
  const [items, setItems] = useState<T[]>([])
  const [loading, setLoading] = useState(true)
  const [open, setOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [mode, setMode] = useState<'create' | 'edit'>('create')
  const editing = useRef<T | null>(null)
  const resRef = useRef(res)
  resRef.current = res

  const load = useCallback(
    async (quiet = false) => {
      if (!quiet) setLoading(true)
      try {
        setItems((await resRef.current.list()).items)
      } catch (e) {
        if (!quiet) message.error(errMsg(e))
      } finally {
        setLoading(false)
      }
    },
    [message],
  )

  useEffect(() => {
    load()
  }, [load])

  const openCreate = (preset?: Partial<T>) => {
    setMode('create')
    editing.current = null
    form.resetFields()
    form.setFieldsValue({ ...blank(), ...preset } as Parameters<typeof form.setFieldsValue>[0])
    setOpen(true)
  }

  const openEdit = (row: T) => {
    setMode('edit')
    editing.current = row
    form.resetFields()
    form.setFieldsValue(structuredClone(row) as Parameters<typeof form.setFieldsValue>[0])
    setOpen(true)
  }

  /** Validates the form, merges it over the edited row (keeps fields the form doesn't show), saves. */
  const save = async (transform?: (t: T) => T): Promise<T | undefined> => {
    let values: T
    try {
      values = (await form.validateFields()) as T
    } catch {
      return
    }
    const base = mode === 'edit' && editing.current ? editing.current : blank()
    const merged = deepMerge(structuredClone(base), values)
    const body = transform ? transform(merged) : merged
    setSaving(true)
    try {
      const out = mode === 'create' ? await resRef.current.create(body) : await resRef.current.update(body)
      message.success(mode === 'create' ? `已新增${label}` : `已保存${label}`)
      setOpen(false)
      await load(true)
      return out
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setSaving(false)
    }
  }

  const remove = (row: T) =>
    modal.confirm({
      title: `删除${label}`,
      content: `确定删除「${row.name ?? row.id}」？此操作不可撤销。`,
      okText: '删除',
      okButtonProps: { danger: true },
      cancelText: '取消',
      onOk: async () => {
        try {
          await resRef.current.remove(row.id)
          message.success('已删除')
          await load(true)
        } catch (e) {
          message.error(errMsg(e))
        }
      },
    })

  /** Saves a single-field change immediately (e.g. a Switch in the table). */
  const patch = async (row: T, change: Partial<T>, transform?: (t: T) => T) => {
    const next = { ...row, ...change }
    setItems((list) => list.map((x) => (x.id === row.id ? next : x)))
    try {
      await resRef.current.update(transform ? transform(next) : next)
      message.success('已更新')
    } catch (e) {
      message.error(errMsg(e))
    }
    load(true)
  }

  return { items, setItems, loading, load, form, open, close: () => setOpen(false), mode, saving, openCreate, openEdit, save, remove, patch }
}

function deepMerge<T>(base: T, over: Partial<T>): T {
  if (typeof base !== 'object' || base === null || Array.isArray(base)) return (over ?? base) as T
  const out = { ...base } as Record<string, unknown>
  for (const [k, v] of Object.entries(over ?? {})) {
    if (v === undefined) continue
    const b = (base as Record<string, unknown>)[k]
    out[k] = typeof v === 'object' && v !== null && !Array.isArray(v) && typeof b === 'object' && b !== null ? deepMerge(b, v) : v
  }
  return out as T
}
