import { getToken, invalidateToken } from './api'
import { useCallback, useEffect, useRef, useState } from 'react'

export interface LiveNote {
  id: string
  path: string
  version: number
  deleted: boolean
  updatedBy: string
}
export type LiveEvent =
  | { type: 'note.created' | 'note.updated' | 'note.deleted'; note: LiveNote }
  | { type: 'presence'; presence: { noteId: string; user: string; state: 'editing' | 'stopped' } }
  | { type: 'hello' }

// Connects to the WebSocket as `user` (idle while blank), reconnecting with 1s -> 5s backoff.
export function useLiveEvents(user: string, onEvent: (e: LiveEvent) => void, onOpen: () => void) {
  const [connected, setConnected] = useState(false)
  const ws = useRef<WebSocket | null>(null)
  const handlers = useRef({ onEvent, onOpen })
  handlers.current = { onEvent, onOpen }

  useEffect(() => {
    let stopped = false
    let delay = 1000
    let timer: number | undefined

    const retry = () => {
      timer = window.setTimeout(connect, delay)
      delay = Math.min(delay * 2, 5000)
    }

    const connect = async () => {
      let token: string
      try {
        token = await getToken(user)
      } catch {
        if (!stopped) retry()
        return
      }
      if (stopped) return
      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      const sock = new WebSocket(`${proto}://${location.host}/api/v1/ws?access_token=${encodeURIComponent(token)}`)
      ws.current = sock
      let opened = false
      sock.onopen = () => {
        opened = true
        delay = 1000
        setConnected(true)
        handlers.current.onOpen()
      }
      sock.onmessage = (m) => handlers.current.onEvent(JSON.parse(m.data))
      sock.onclose = () => {
        setConnected(false)
        if (stopped) return
        // Never opened: probably a rejected token (server restart), so mint a new one next time.
        if (!opened) invalidateToken(user)
        retry()
      }
    }
    if (!user) return
    connect()
    return () => {
      stopped = true
      clearTimeout(timer)
      ws.current?.close()
    }
  }, [user])

  const send = useCallback((msg: object) => {
    if (ws.current?.readyState === WebSocket.OPEN) ws.current.send(JSON.stringify(msg))
  }, [])

  return { connected, send }
}
