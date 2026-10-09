import { useCallback, useEffect, useRef, useState } from 'react'
import { ApiError, Note, createNote, getNote, listNotes, saveNote } from './api'
import { LiveEvent, useLiveEvents } from './useLiveEvents'

// Autosave waits this long after the last keystroke, so a burst of typing is one PUT, not one per key.
const SAVE_DELAY_MS = 150
// "editing" presence clears after this much idle time, since there is no Save button to end it.
const IDLE_MS = 3000

const loadName = () => {
  try {
    return sessionStorage.getItem('name') ?? ''
  } catch {
    return ''
  }
}

export default function App() {
  const [user, setUser] = useState(loadName)
  // The input edits a draft; the name (and so the WebSocket identity) only changes on blur/Enter.
  const [nameDraft, setNameDraft] = useState(user)
  const [notes, setNotes] = useState<Note[]>([])
  const [presence, setPresence] = useState(new Map<string, Set<string>>())
  const [open, setOpen] = useState<Note | null>(null) // the version the user loaded
  const [text, setText] = useState('')
  const [conflict, setConflict] = useState<Note | null>(null)
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  // Off = save manually, which makes a 412 conflict easy to reproduce in a demo.
  const [autosave, setAutosave] = useState(true)
  const editing = useRef<string | null>(null) // note id we told the server we are editing
  const idleTimer = useRef<number | undefined>(undefined)

  const dirty = open !== null && text !== open.body
  // Live-event handlers outlive renders, so they read current state through this ref.
  const latest = useRef({ user, open, dirty })
  latest.current = { user, open, dirty }

  const fail = (e: unknown) => setError(e instanceof Error ? e.message : String(e))
  const refreshList = useCallback(
    () => listNotes(latest.current.user).then(setNotes).catch(fail),
    [],
  )

  const adopt = (n: Note) => {
    setOpen(n)
    setText(n.body)
    setConflict(null)
    setNotice('')
    setError('')
  }

  const onEvent = (e: LiveEvent) => {
    if (e.type === 'presence') {
      const { noteId, user: who, state } = e.presence
      setPresence((prev) => {
        const next = new Map(prev)
        const set = new Set(next.get(noteId))
        if (state === 'editing') set.add(who)
        else set.delete(who)
        set.size ? next.set(noteId, set) : next.delete(noteId)
        return next
      })
    } else if (e.type !== 'hello') {
      refreshList()
      const { open, dirty, user } = latest.current
      const n = e.note
      if (!open || n.id !== open.id) return
      if (e.type === 'note.deleted' || n.deleted) {
        setNotice(`${n.updatedBy} deleted this note`)
      } else if (n.updatedBy !== user && n.version > open.version) {
        if (dirty) setNotice(`updated by ${n.updatedBy} — your save will conflict`)
        else getNote(user, n.id).then(adopt).catch(fail)
      }
    }
  }

  const { connected, send } = useLiveEvents(
    user,
    onEvent,
    useCallback(() => {
      setPresence(new Map()) // presence is lost across a reconnect
      refreshList()
    }, [refreshList]),
  )

  const stopEditing = () => {
    clearTimeout(idleTimer.current)
    if (editing.current) send({ type: 'stopped', noteId: editing.current })
    editing.current = null
  }

  const select = async (id: string) => {
    stopEditing()
    try {
      adopt(await getNote(user, id))
    } catch (e) {
      fail(e)
    }
  }

  const create = async () => {
    const path = prompt('Path for the new note (e.g. eng/todo.md)')
    if (!path) return
    try {
      const n = await createNote(user, path)
      await refreshList()
      await select(n.id)
    } catch (e) {
      fail(e)
    }
  }

  const onType = (value: string) => {
    setText(value)
    if (!open) return
    if (editing.current !== open.id) {
      editing.current = open.id
      send({ type: 'editing', noteId: open.id })
    }
    clearTimeout(idleTimer.current)
    idleTimer.current = window.setTimeout(stopEditing, IDLE_MS)
  }

  // `base` is the note whose version/path/tags we write over (ours, or theirs when overwriting).
  const save = async (base: Note) => {
    setError('')
    setSaving(true)
    try {
      const saved = await saveNote(user, base.id, { path: base.path, body: text, tags: base.tags }, base.version)
      if (latest.current.open?.id !== base.id) return // user switched notes mid-save
      setOpen(saved)
      setConflict(null)
      setNotice('')
    } catch (e) {
      if (e instanceof ApiError && e.status === 412 && e.current) setConflict(e.current)
      else fail(e)
    } finally {
      setSaving(false)
    }
  }

  // Autosave: one PUT in flight at a time. Each PUT must carry the version the
  // previous one returned, so firing per keystroke would 412 against ourselves.
  // Text typed during a save leaves the editor dirty, which re-triggers this.
  // A conflict pauses autosave until the user picks a resolution.
  useEffect(() => {
    if (!autosave || !open || !dirty || saving || conflict) return
    const t = window.setTimeout(() => save(open), SAVE_DELAY_MS)
    return () => clearTimeout(t)
  }, [autosave, open, text, dirty, saving, conflict])

  const rename = (name: string) => {
    name = name.trim()
    if (name === user) return
    setUser(name)
    try {
      sessionStorage.setItem('name', name)
    } catch {
      /* storage unavailable: name just won't persist */
    }
  }

  const sorted = [...notes].sort((a, b) => a.path.localeCompare(b.path))

  return (
    <div className="app">
      <header>
        <label>
          Your name{' '}
          <input
            value={nameDraft}
            onChange={(e) => setNameDraft(e.target.value)}
            onBlur={() => rename(nameDraft)}
            onKeyDown={(e) => e.key === 'Enter' && rename(nameDraft)}
            placeholder="alice"
          />
        </label>
        <span className={`dot ${connected ? 'on' : 'off'}`} />
        {!user ? 'not signed in' : connected ? `connected as ${user}` : `reconnecting as ${user}…`}
        <label>
          <input type="checkbox" checked={autosave} onChange={(e) => setAutosave(e.target.checked)} /> Autosave
        </label>
      </header>
      <aside>
        {!user ? (
          <p className="muted">Enter your name to start</p>
        ) : (
          <>
        <button onClick={create}>+ New note</button>
        <ul>
          {sorted.map((n) => {
            const others = [...(presence.get(n.id) ?? [])].filter((u) => u !== user)
            return (
              <li key={n.id} className={n.id === open?.id ? 'sel' : ''} onClick={() => select(n.id)}>
                <div className="path">{n.path}</div>
                <small>
                  v{n.version} · {n.updatedBy}
                  {others.map((u) => (
                    <span key={u} className="badge">✎ {u}</span>
                  ))}
                </small>
              </li>
            )
          })}
        </ul>
          </>
        )}
      </aside>
      <main>
        {!user || !open ? (
          <p className="muted">Select a note.</p>
        ) : (
          <>
            <h2>
              {open.path} <small>v{open.version} · {saving ? 'saving…' : dirty ? (autosave ? 'saving…' : 'unsaved') : 'saved'}</small>
            </h2>
            {conflict && (
              <div className="banner conflict">
                {conflict.updatedBy} saved version {conflict.version} since you opened this
                <button onClick={() => adopt(conflict)}>Load their version</button>
                <button onClick={() => save(conflict)}>Overwrite with mine</button>
              </div>
            )}
            {notice && <div className="banner notice">{notice}</div>}
            {error && <div className="banner error">{error}</div>}
            <textarea value={text} onChange={(e) => onType(e.target.value)} />
            {!autosave && (
              <button onClick={() => save(open)} disabled={!dirty || saving}>
                Save
              </button>
            )}
          </>
        )}
        {!open && error && <div className="banner error">{error}</div>}
      </main>
    </div>
  )
}
