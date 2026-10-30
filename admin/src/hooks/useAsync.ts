'use client'
import { App } from 'antd'
import { useCallback, useEffect, useRef, useState } from 'react'
import { errMsg } from '@/lib/format'

/** Loads data on mount (and on reload()), surfacing errors as toasts. */
export function useAsync<T>(fn: () => Promise<T>, deps: unknown[] = []) {
  const { message } = App.useApp()
  const [data, setData] = useState<T>()
  const [loading, setLoading] = useState(true)
  const fnRef = useRef(fn)
  fnRef.current = fn

  const reload = useCallback(
    async (quiet = false) => {
      if (!quiet) setLoading(true)
      try {
        const d = await fnRef.current()
        setData(d)
        return d
      } catch (e) {
        if (!quiet) message.error(errMsg(e))
      } finally {
        setLoading(false)
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [message, ...deps],
  )

  useEffect(() => {
    reload()
  }, [reload])

  return { data, setData, loading, reload }
}

/** Runs an action with a success toast; returns true on success. */
export function useAction() {
  const { message } = App.useApp()
  const [busy, setBusy] = useState(false)
  const run = useCallback(
    async (fn: () => Promise<unknown>, ok?: string) => {
      setBusy(true)
      try {
        await fn()
        if (ok) message.success(ok)
        return true
      } catch (e) {
        message.error(errMsg(e))
        return false
      } finally {
        setBusy(false)
      }
    },
    [message],
  )
  return { busy, run }
}
