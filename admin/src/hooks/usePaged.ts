'use client'
import { App } from 'antd'
import { useCallback, useEffect, useRef, useState } from 'react'
import type { ListQuery, Page } from '@/lib/api'
import { errMsg } from '@/lib/format'

/**
 * Server-side paginated list (users, orders): filters + page/size + loading.
 * Changing a filter via setFilter goes back to page 1 and reloads.
 *
 *   const t = usePaged(api.users.list, { q: '', status: '' })
 *   <Table {...t.tableProps} />
 */
export function usePaged<T, F extends { q?: string; status?: string }>(
  fetcher: (q: ListQuery) => Promise<Page<T>>,
  initialFilters: F,
  initialSize = 20,
) {
  const { message } = App.useApp()
  const [items, setItems] = useState<T[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(false)
  const [filters, setFilters] = useState<F>(initialFilters)
  const [pager, setPager] = useState({ page: 1, size: initialSize })
  const fetchRef = useRef(fetcher)
  fetchRef.current = fetcher

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await fetchRef.current({ ...filters, page: pager.page, size: pager.size })
      setItems(res.items)
      setTotal(res.total)
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setLoading(false)
    }
  }, [filters, pager, message])

  useEffect(() => {
    load()
  }, [load])

  const setFilter = <K extends keyof F>(k: K, v: F[K]) => {
    setFilters((f) => ({ ...f, [k]: v }))
    setPager((p) => ({ ...p, page: 1 }))
  }

  return {
    items,
    total,
    loading,
    filters,
    setFilter,
    reload: load,
    pagination: {
      current: pager.page,
      pageSize: pager.size,
      total,
      showSizeChanger: true,
      pageSizeOptions: [20, 50, 100],
      showTotal: (n: number) => `共 ${n} 条`,
      onChange: (page: number, size: number) => setPager({ page: size === pager.size ? page : 1, size }),
    },
  }
}
