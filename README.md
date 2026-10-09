# Shared Notes Service

A REST backend for a note-taking service shared by several small teams. It's written in Go with Echo and stores notes in MongoDB. Clients get live change notifications and "someone is editing this" presence over a WebSocket.

Notes are Markdown documents addressed by a folder-style path such as `eng/runbooks/deploy.md`. There is one shared namespace, and teams organize themselves with top-level folders. Concurrent edits are detected with optimistic versioning (`ETag` / `If-Match`), never silently overwritten.

## Quick start

Requirements: Go 1.27+, Docker with the compose plugin, and curl.

```sh
# Everything in containers: MongoDB + the service on :8080
docker compose up --build -d
curl localhost:8080/healthz

# Or run the service locally against the compose MongoDB
docker compose up -d mongo
cp .env.example .env       # optional, the defaults already match compose
make run
```

Watch live events in a second terminal, then drive the API with the smoke script:

```sh
go run ./cmd/notes-watch -prefix eng/ -user watcher
bash scripts/smoke.sh      # or: make smoke
```

The smoke script walks through create, read, update, a stale update (412), a missing precondition (428), list, folders, delete and a final 404. It fails loudly on any unexpected status. The watcher prints one JSON line per event as the script runs.

## Configuration

Settings come from environment variables, and an optional `.env` file fills in anything not already set. See `.env.example`.

**`HTTP_ADDR`**: listen address, default `:8080`.

**`MONGO_URI`**: default `mongodb://localhost:27017`.

**`MONGO_DB`**: default `notes`. Pointing a second instance at a different database fully isolates a team's notes.

**`MONGO_NOTES_COLLECTION`**: default `notes`.

**`LOG_LEVEL`**: `debug`, `info`, `warn` or `error`, default `info`.

**`MONGO_HOST_PORT`**: compose only, the host port for the Mongo container. Change it if a local mongod already uses 27017.

## Testing

```sh
docker compose up -d mongo
make test        # everything, with -race: unit, store conformance on Mongo, e2e
make test-unit   # -short: no MongoDB needed
make test-e2e    # contract tests only
make cover       # writes coverage.html (clickable per-file, per-line report)
```

The tests come in three layers:

**Unit tests** cover path and tag rules, the service, the event hub, and every HTTP status code. HTTP tests go through `httptest` with an in-memory store, so they need no database.

**Store conformance** is one suite (`internal/store/storetest`) that runs against both the in-memory store and the MongoDB store. The fake used by the unit tests is therefore held to exactly the same contract as the real store, and the two can't drift apart.

**Contract (e2e) tests** (`test/e2e`) build the production wiring (`internal/app`, which `cmd/notesd` also uses) against a throwaway MongoDB database. They talk to it only over HTTP and a real WebSocket client. Scenarios include a two-writer conflict, rename and path collision, pagination, reconnect catch-up, and presence between two clients.

Mongo-backed tests skip with a message when `MONGO_URI` is unset. The Makefile defaults it to the compose instance. `make cover` measures `./internal/...` across all test layers together; it's about 90% at the time of writing.

## API

Base path `/api/v1`. Identity comes from the optional `X-User-ID` header, recorded as `createdBy`/`updatedBy` and defaulting to `anonymous`. It's an unauthenticated placeholder for real auth (see below). Every error has the same JSON shape: `{"error": {"code": "...", "message": "..."}}`.

| Method & path | Purpose | Notes |
|---|---|---|
| `GET /healthz` | Liveness and database ping | 200 or 503 |
| `POST /notes` | Create `{path, body, tags}` | 201, `ETag`, `Location`; 409 if the path is taken |
| `GET /notes` | List / search / sync | `prefix`, `tag`, `q`, `updatedSince`, `includeDeleted`, `limit` (≤200), `cursor`, `fields=body` |
| `GET /notes/:id` | Read one note | `ETag`; `If-None-Match` gives 304 |
| `PUT /notes/:id` | Replace `{path, body, tags}` | Requires `If-Match`; a changed `path` renames or moves |
| `DELETE /notes/:id` | Soft delete | Requires `If-Match`; 204 |
| `GET /folders?prefix=eng/` | Immediate child folders with note counts | Drives a file-tree UI |
| `GET /ws?prefix=eng/&user=alice` | WebSocket event stream | See below |

Write status codes: 400 for invalid input, 404 when the note is missing or deleted, 409 when the path is taken, 412 for a stale `If-Match` (the body includes `current`, the latest note, and `ETag` carries its version), 413 for a body over 1 MiB, and 428 when `If-Match` is missing.

```sh
# Create
curl -si localhost:8080/api/v1/notes -H 'X-User-ID: alice' \
  -d '{"path":"eng/runbooks/deploy.md","body":"# Deploy\n","tags":["ops"]}'
#   HTTP/1.1 201 Created
#   Etag: "1"

# Update with the version you last read
curl -si -X PUT localhost:8080/api/v1/notes/<id> -H 'X-User-ID: bob' -H 'If-Match: "1"' \
  -d '{"path":"eng/runbooks/deploy.md","body":"# Deploy v2\n","tags":["ops"]}'
#   HTTP/1.1 200 OK, Etag: "2". Repeating it with If-Match "1" now gives 412.

# Browse and search
curl -s 'localhost:8080/api/v1/folders?prefix=eng/'
curl -s 'localhost:8080/api/v1/notes?prefix=eng/&tag=ops&fields=body'
curl -s 'localhost:8080/api/v1/notes?q=deploy'
```

**Lists** are sorted by `(updatedAt, id)` ascending and paginated with an opaque keyset `cursor`, returned as `nextCursor`. Ascending order makes the same endpoint double as a sync feed: `?updatedSince=<last seen>&includeDeleted=true` returns everything that changed, including deletions.

**WebSocket.** After connecting you get a `hello` frame. The server then pushes `note.created`, `note.updated` and `note.deleted` events for paths under `prefix`. Events carry metadata only, no body: the client refetches the body when it needs it. A note moved out of a folder is delivered to subscribers of both its old and new folders, with `previousPath` set.

Clients may send `{"type":"editing","noteId":"..."}` and `{"type":"stopped","noteId":"..."}`. These are relayed to other subscribers as `presence` events and never stored. A disconnect automatically sends `stopped` for anything that connection had open. `user` is a query parameter here because browsers can't set headers on a WebSocket handshake.

```json
{"type":"note.updated","note":{"id":"…","path":"eng/runbooks/deploy.md","title":"deploy.md","version":2,"tags":["ops"],"deleted":false,"updatedAt":"…","updatedBy":"bob"}}
{"type":"presence","presence":{"noteId":"…","path":"eng/runbooks/deploy.md","user":"alice","state":"editing","at":"…"}}
```

## Layout

```
cmd/notesd          service entry point (config, Mongo connection, graceful shutdown)
cmd/notes-watch     tiny WebSocket client that prints events; a stand-in for websocat
internal/app        production wiring shared by notesd and the e2e tests
internal/config     .env + environment loading
internal/note       domain: Note, path/tag rules, Repository interface, Service
internal/store      memstore (tests), mongostore (production), storetest (shared conformance suite)
internal/events     in-process pub/sub hub for note changes and presence
internal/httpapi    Echo router, handlers, error mapping, ETags, WebSocket handler
test/e2e            black-box contract tests against real MongoDB
scripts/smoke.sh    curl walkthrough of the API
```

Dependencies point inward. `note` knows nothing about Mongo, HTTP or WebSockets; stores implement `note.Repository`, and the hub implements `note.Publisher`.

## Design choices I spent the most time on

### 1. Optimistic versioning instead of merging or last-write-wins

Every note has an integer `version`, exposed as its `ETag`. `PUT` and `DELETE` must send `If-Match`. The store applies the write as a single `findOneAndUpdate` filtered on `{_id, version, deleted: false}`, so the version check and the write are atomic. That needs no transactions and no replica set. A stale write gets **412** with the current note in the body, so the client has everything it needs to show a diff and retry. A missing `If-Match` gets **428** instead of being treated as "overwrite", because "I forgot the header" shouldn't silently erase someone's work.

**Why not last-write-wins:** it's the simplest option, but it silently loses edits, which is the one thing a shared notes tool must not do.

**Why not server-side merging:** this is what Obsidian's Self-hosted LiveSync does. It keeps a revision tree and runs a three-way, line-level merge against the common ancestor, falling back to asking the user when edits overlap. It's the right answer for single-user multi-device sync, where offline edits are normal. These teams' notes are mostly written once and read many times, and live presence lets people see someone else is editing and step away. Under those conditions, rejecting the rare collision and letting the client resolve it is far less code and far fewer edge cases. Merging on the server is the first item in future work if collisions turn out to be common.

### 2. A flat namespace addressed by path, with immutable IDs

Instead of modeling teams, workspaces and memberships before auth exists, notes live in one namespace and are organized by folder-style paths. Folders are implied by path prefixes, as in S3, and `GET /folders` derives the tree with an aggregation. Teams claim top-level folders. If a team needs hard isolation, a second instance pointed at another `MONGO_DB` gives it with zero shared state. This matches how Obsidian vaults and LiveSync model notes.

Two details took deliberate thought:

**Immutable UUIDs, with the path as a mutable field.** LiveSync derives document IDs from paths, which makes a rename a delete plus a create. Here a rename or move is just a `PUT` with a new `path`. It keeps its history, its version and its identity, and live subscribers of both the old and new folders hear about it.

**Path uniqueness that respects soft deletes.** There's a unique index on `path` with a partial filter `{deleted: false}`, so live paths can't collide but a deleted note's path can be reused right away. Path rules live in one heavily tested function: trim, collapse `//`, reject `.`/`..`, control characters, and overlong paths or segments.

### 3. REST for writes, WebSocket for push, with a deliberately simple in-process hub

All writes go through REST, which keeps a single validation and concurrency path and makes everything testable with curl. The WebSocket only pushes metadata events and carries small, ephemeral presence messages. Sending metadata without bodies keeps fan-out cheap, and clients refetch what they actually display. A slow subscriber is disconnected (close code 1013) rather than allowed to block writers. It reconnects and catches up with `GET /notes?updatedSince=…&includeDeleted=true`, which is why deletes are soft deletes that appear in that feed.

The hub lives in memory inside one process. That was a conscious tradeoff: it's simple, fully unit-testable, and works with a standalone MongoDB. The cost is that **it only works with a single instance**. Running two replicas behind a load balancer would mean each one only notifies its own clients. The fix is to drive the hub from MongoDB change streams, which need a replica set, or from Redis pub/sub. That change is contained to `internal/events` and the wiring in `internal/app`, because the service only knows about `note.Publisher`.

## What I'd change or add with more time

**Authentication and authorization.** Replace the `X-User-ID` header (one helper, `actor()` in `internal/httpapi/notes.go`) with OIDC/JWT verification. Add folder-level ACLs, so that "team folders" become enforced rather than just a convention, and filter WebSocket events by permission. Lock down WebSocket origin checks and CORS at the same time; both are wide open today.

**A sequence-numbered change log** in place of timestamp catch-up. `updatedSince` has a small, real gap. Two writers can take their timestamps in one order and commit in the other, so a reconnecting client can occasionally miss a change. A monotonically increasing sequence, from a counter document or from change-stream resume tokens, closes it. It's the same idea as CouchDB's `_changes?since=seq`, which LiveSync relies on.

**Horizontal scale:** change streams or Redis for the hub, as described above.

**Revision history and server-side merging.** Store prior versions, which enables "restore" and a three-way merge using a diff library such as `sergi/go-diff`. Then the client's 412 path can become "auto-merged unless the edits overlap".

**Tombstone purge.** Soft-deleted notes are kept forever. A periodic job should purge them once every client has had time to sync, plus a trash/restore endpoint.

**Operational basics:** rate limiting, request metrics and tracing, a readiness probe separate from liveness, and an OpenAPI spec generated or checked in CI.

**Attachments.** Images and PDFs belong in object storage, with only references kept in notes.

**Stop doing:** asking every client to send the full note body on `PUT`. For large notes, a `PATCH` with a text diff would cut bandwidth. It only becomes worth its complexity alongside the merge work above.

## Alternatives considered

**Just deploy Self-hosted LiveSync.** For a team already on Obsidian this is arguably the right answer: [self-hosted-livesync-server](https://github.com/vrtmrz/self-hosted-livesync-server) is a docker-compose file for CouchDB behind Caddy, and the [Obsidian LiveSync plugin](https://github.com/vrtmrz/obsidian-livesync) does the syncing, conflict merging and optional end-to-end encryption. Its limits for this brief: it's designed as single-user, multi-device sync, with access control only at the level of a whole database; it ties every user to Obsidian; and it offers no API for a React or Electron client to build on. This service borrows its data model (path-addressed notes, logical deletes) and its "detect conflicts, don't guess" stance, but exposes them as a plain REST API with explicit sharing in mind.

**Managed cloud services.** In production I'd look at MongoDB Atlas (managed replica sets, which also unlocks change streams) or DynamoDB with Streams, and a managed pub/sub layer such as AWS API Gateway WebSockets, Ably or Pusher instead of the in-process hub. I left them out to keep the assessment self-contained and runnable with one `docker compose up`.

**SQL (Postgres).** It would work well, and it makes the sequence-numbered change log trivial with a `BIGSERIAL`. I chose MongoDB because notes are naturally single documents with no relations, and an atomic conditional `findOneAndUpdate` is all that versioning needs.

**Server-Sent Events instead of WebSockets.** SSE is simpler for one-way push and reconnects on its own. I chose WebSockets because presence is genuinely client-to-server, and it leaves room for richer collaboration later.

## Commit history

The history is meant to be read phase by phase, one commit per layer, followed by the README:

```
git log --oneline --reverse
```

```
5adcf51 Initial commit
db729b8 Scaffold service: config, healthz, error envelope, compose, Makefile
f40c46d Add note domain: path rules, versioned service, in-memory store, shared store conformance suite
a6d1f11 Add MongoDB store with atomic compare-and-set writes and indexes; wire into notesd
b6d64ae Add notes REST API: CRUD with ETag/If-Match concurrency, keyset pagination, folders
2ee38b1 Add live updates: in-process event hub, WebSocket push with prefix filters and presence, notes-watch client
d8bd594 Route moved notes to old and new prefixes; close WebSockets with 1001 on shutdown; go mod tidy
9b6f358 Add e2e contract tests over shared app wiring, coverage report, curl smoke script
```
