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

// Connects to the WebSocket as `user`, reconnecting with 1s -> 5s backoff.
export function useLiveEvents(user: string, onEvent: (e: LiveEvent) => void, onOpen: () => void) {
  const [connected, setConnected] = useState(false)
  const ws = useRef<WebSocket | null>(null)
  const handlers = useRef({ onEvent, onOpen })
  handlers.current = { onEvent, onOpen }

  useEffect(() => {
    let stopped = false
    let delay = 1000
    let timer: number | undefined

    const connect = () => {
      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      const sock = new WebSocket(`${proto}://${location.host}/api/v1/ws?user=${encodeURIComponent(user || 'anonymous')}`)
      ws.current = sock
      sock.onopen = () => {
        delay = 1000
        setConnected(true)
        handlers.current.onOpen()
      }
      sock.onmessage = (m) => handlers.current.onEvent(JSON.parse(m.data))
      sock.onclose = () => {
        setConnected(false)
        if (stopped) return
        timer = window.setTimeout(connect, delay)
        delay = Math.min(delay * 2, 5000)
      }
    }
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
