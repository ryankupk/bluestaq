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

// Dev tokens are minted by name, cached in memory and sessionStorage (survives reloads).
const tokens = new Map<string, string>()
const storeKey = (user: string) => `token:${user}`

export async function getToken(user: string): Promise<string> {
  let token = tokens.get(user)
  try {
    token ??= sessionStorage.getItem(storeKey(user)) ?? undefined
  } catch {
    /* storage unavailable: token just won't survive a reload */
  }
  if (token) {
    tokens.set(user, token)
    return token
  }
  const res = await fetch('/api/v1/auth/dev-token', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ user }),
  })
  const json = await res.json().catch(() => null)
  if (!res.ok) throw new ApiError(res.status, json?.error?.message ?? res.statusText)
  tokens.set(user, json.token)
  try {
    sessionStorage.setItem(storeKey(user), json.token)
  } catch {
    /* see above */
  }
  return json.token
}

// Called when the server rejected a token (restart with a new secret, or expiry).
export function invalidateToken(user: string) {
  tokens.delete(user)
  try {
    sessionStorage.removeItem(storeKey(user))
  } catch {
    /* see above */
  }
}

async function call<T>(user: string, method: string, url: string, body?: NoteInput, ifMatch?: number): Promise<T> {
  const send = async () => {
    const headers: Record<string, string> = { Authorization: `Bearer ${await getToken(user)}` }
    if (body) headers['Content-Type'] = 'application/json'
    if (ifMatch !== undefined) headers['If-Match'] = `"${ifMatch}"`
    return fetch(`/api/v1${url}`, { method, headers, body: body && JSON.stringify(body) })
  }
  let res = await send()
  if (res.status === 401) {
    invalidateToken(user)
    res = await send() // once, with a fresh token
  }
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
