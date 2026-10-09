export interface Note {
  id: string
  path: string
  title: string
  body: string
  tags: string[]
  version: number
  deleted: boolean
  updatedAt: string
  updatedBy: string
}

export interface NoteInput {
  path: string
  body: string
  tags: string[]
}

// 412 carries the latest note in `current`.
export class ApiError extends Error {
  constructor(public status: number, message: string, public current?: Note) {
    super(message)
  }
}

async function call<T>(user: string, method: string, url: string, body?: NoteInput, ifMatch?: number): Promise<T> {
  const headers: Record<string, string> = { 'X-User-ID': user || 'anonymous' }
  if (body) headers['Content-Type'] = 'application/json'
  if (ifMatch !== undefined) headers['If-Match'] = `"${ifMatch}"`
  const res = await fetch(`/api/v1${url}`, { method, headers, body: body && JSON.stringify(body) })
  const json = await res.json().catch(() => null)
  if (!res.ok) throw new ApiError(res.status, json?.error?.message ?? res.statusText, json?.current)
  return json as T
}

export const listNotes = (user: string) =>
  call<{ items: Note[] }>(user, 'GET', '/notes?fields=body&limit=200').then((r) => r.items)
export const getNote = (user: string, id: string) => call<Note>(user, 'GET', `/notes/${id}`)
export const createNote = (user: string, path: string) =>
  call<Note>(user, 'POST', '/notes', { path, body: '', tags: [] })
export const saveNote = (user: string, id: string, input: NoteInput, version: number) =>
  call<Note>(user, 'PUT', `/notes/${id}`, input, version)
