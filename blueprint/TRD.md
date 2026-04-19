# autowiki — Technical Requirements Document

## 1. Architecture Overview

```
Browser (Remix SPA)
      ↕  HTTP + SSE
  Go HTTP Server
      ├── /api/*          → API handlers
      └── /*              → public/index.html (SPA fallback)
           ↕
     Claude Sonnet (Anthropic API)
           ↕
     Obsidian Vault (markdown files on disk)
     Pebble (chat history)
```

The Go server is the single process. It serves the frontend, handles all API calls, manages vault reads/writes, and runs the dream state goroutine.

---

## 2. Tech Stack

| Layer | Technology |
|---|---|
| Backend | Go |
| Frontend | React Router (Remix) |
| LLM | Claude Sonnet (`claude-sonnet-4-6`) via Anthropic API |
| Auth | Google OAuth 2.0 (`golang.org/x/oauth2`) |
| Sessions | Signed HTTP-only cookies (session token → Pebble) |
| Chat history | Pebble (`cockroachdb/pebble` Go bindings) |
| Wiki storage | Markdown files (Obsidian vault on local disk) |
| Streaming | Server-Sent Events (SSE) |
| Build | Makefile |

---

## 3. Project Structure

```
autowiki/
├── cmd/server/main.go
├── internal/
│   ├── server/        # HTTP routing, static file serving, SPA fallback
│   ├── auth/          # Google OAuth flow, session validation, email whitelist
│   ├── chat/          # SSE streaming, session management, intent detection
│   ├── vault/         # Read/write markdown, wikilinks, attachments, index/log
│   ├── llm/           # Claude API client, prompt templates
│   ├── store/         # Pebble — chat sessions, message history, auth sessions
│   └── dream/         # Background goroutine, nightly scheduler (configurable UTC window)
├── web/               # Remix app source
│   └── app/
├── public/            # Built Remix output — served by Go
├── blueprint/         # PRD, TRD
├── config.yaml
└── Makefile

# The Obsidian vault lives outside the repo at the path set in config.yaml.
# internal/vault is Go code (the vault I/O package), not vault data.

---

## 4. System Components

### 4.1 Go HTTP Server (`internal/server`)

- Serves the built Remix SPA from `public/` for all non-`/api` routes. SPA fallback: always return `public/index.html` for unmatched routes.
- Routes all `/api/*` requests to the appropriate handler.
- In development mode, proxies non-`/api` traffic to the Remix dev server instead of serving from `public/`.
- Single binary, single instance.

### 4.2 Auth (`internal/auth`)

- Implements Google OAuth 2.0 using `golang.org/x/oauth2`.
- On successful Google sign-in, the returned email is checked against `allowed_email` in `config.yaml`. Any mismatch returns a 403 and does not create a session.
- On success, a signed HTTP-only session cookie is issued. The session token is stored in Pebble with an expiry.
- An auth middleware wraps all `/api/*` routes (except the OAuth endpoints themselves). Unauthenticated requests to `/api/*` return 401. Unauthenticated requests to any other route are redirected to the sign-in page.
- The Remix frontend has a single `/login` route that renders the "Sign in with Google" button. All other routes require a valid session.

### 4.3 Chat & SSE Handler (`internal/chat`)

- Accepts `POST /api/chat` as `multipart/form-data` (text message + optional file attachments).
- Resolves or creates the active session before processing (see session logic in §5).
- Passes message and attachments to the LLM client.
- Streams the LLM response back to the client via Server-Sent Events.
- After streaming completes, triggers vault write evaluation asynchronously.

### 4.4 LLM Client (`internal/llm`)

Wraps the Anthropic Claude Sonnet API. Exposes three prompt pipelines:

- **Ingest**: Given a user message + attachments + current `index.md` + relevant page contents → determine what vault writes are needed and produce updated page content.
- **Query**: Given a question + relevant page contents → produce a synthesized answer with `[[citations]]`.
- **Dream**: Given a full vault snapshot → identify and produce reorganization edits.

All pipelines receive `schema.md` as part of the system prompt to enforce wiki conventions.

### 4.5 Vault Manager (`internal/vault`)

- Reads and writes `.md` files to the configured Obsidian vault path.
- Parses and resolves `[[wikilinks]]`.
- Manages special files: `index.md` (MOC), `log.md` (append-only activity log).
- Stores raw uploaded files in `_attachments/` and returns their vault-relative path.
- Determines relevant pages for a given query by scanning `index.md` and matching headings/links.

### 4.6 Chat History Store (`internal/store`)

- Uses Pebble as the storage engine via `cockroachdb/pebble` Go bindings.
- Key schema:

  ```
  sessions:list                → ordered list of session IDs (newest first)
  sessions:{id}:meta           → JSON: { id, created_at, last_active_at, title }
  sessions:{id}:messages       → ordered list of message IDs
  messages:{id}                → JSON: { id, session_id, role, content, attachments[], created_at }
  ```

- Session title is auto-generated by the LLM from the first message of the session.

### 4.7 Dream State (`internal/dream`)

- A goroutine launched at server boot.
- Sleeps until a random time within a configurable UTC window (default 19:00–23:00 UTC, ≈ 1–5 am IST). Set via `dream.start_hour_utc` / `dream.end_hour_utc` in `config.yaml`.
- Runs at most once per calendar day (UTC): checks the last 500 chars of `log.md` for today's date before running.
- Uses `dream.Consolidate(ctx, vm, streamer)`: creates an ephemeral `MemChatStore` session, runs the full agentic loop via `chat.AgenticRunner` (capped at 50 tool calls), then makes a separate summary LLM call.
- Appends two single-line entries to `log.md`: `dream started` at the beginning, and `dream ended - <20-word summary>` on completion (or `dream ended - error: ...` on failure).
- Can also be triggered manually via `POST /api/dream/run` (returns 202, runs in background).

### 4.8 Remix Frontend (`web/`)

- Built with React Router (Remix) as a single-page application.
- Built output is placed in `public/` and served statically by the Go server.
- Features:
  - Streaming chat interface (consumes SSE from `/api/chat`).
  - File and image attachment support (drag-and-drop or file picker).
  - Infinite scroll chat history — presents all messages as a single unbroken timeline. Older messages are fetched a session at a time as the user scrolls up; session boundaries are invisible to the user.
  - Vault change summary rendered inline after responses that triggered writes.

---

## 5. Session Management

Session boundary is determined by inactivity:

- On each incoming message, read `last_active_at` of the current session from Pebble.
- If `last_active_at` is more than **24 hours** ago (or no session exists), create a new session.
- Otherwise, append the message to the current session and update `last_active_at`.

---

## 6. Attachment Handling

1. File received via `/api/chat` multipart upload.
2. File copied as-is to `vault/_attachments/{timestamp}-{original-filename}`.
3. Claude extracts a text description/summary (vision for images, text extraction for PDFs).
4. Description used as content for wiki ingest. Page references the file via Obsidian embed syntax: `![[_attachments/...]]`.
5. Raw file is never discarded.

---

## 7. API Specification

### `GET /api/auth/login`

Redirects the browser to Google's OAuth consent screen.

---

### `GET /api/auth/callback`

Google redirects here after consent. The server:
1. Exchanges the code for a token.
2. Fetches the user's email from Google.
3. Checks the email against `allowed_email` in config — rejects with 403 if it does not match.
4. Creates a signed session token, stores it in Pebble with expiry, and sets it as an HTTP-only cookie.
5. Redirects to `/`.

---

### `POST /api/auth/logout`

Deletes the session token from Pebble and clears the cookie.

---

### `POST /api/chat`

Streaming chat endpoint.

**Request** (`multipart/form-data`):
```
message      string    User's text message
session_id   string?   If omitted, server resolves or creates session
files[]      file?     Optional attachments (images, PDFs, documents)
```

**Response**: `text/event-stream` (SSE)

```
event: delta    data: { "text": "..." }                      # streaming text chunk
event: vault    data: { "changes": [{ "type": "created|updated", "path": "..." }] }
event: done     data: { "session_id": "..." }                # stream complete
event: error    data: { "message": "..." }
```

---

### `GET /api/sessions`

Returns the list of session IDs and metadata, newest first. Used by the frontend as a pagination index for infinite scroll — not exposed as a concept in the UI.

**Response**:
```json
{
  "sessions": [
    { "id": "...", "created_at": "...", "last_active_at": "..." }
  ]
}
```

---

### `GET /api/sessions/:id`

Returns the full message history for a single session. The frontend fetches sessions one at a time as the user scrolls up, prepending each batch to the visible timeline.

**Response**:
```json
{
  "messages": [
    { "id": "...", "role": "user|assistant", "content": "...", "attachments": [], "created_at": "..." }
  ]
}
```

---

### `POST /api/dream/run`

Triggers an immediate dream consolidation in the background (auth required).

**Response**: `202 Accepted` (no body). Progress and result are appended to `log.md`.

---

## 8. Prompt Caching

Every request to the Anthropic API includes a large, static payload: the system prompt (identity, instructions, tool-use guidance, vault index) and the full tool definitions for `read_page`, `search_vault`, and `save_to_vault`. This content is identical across all turns in a conversation and changes only when the vault index is updated.

Anthropic's prompt caching feature allows these static portions to be cached server-side, so they are not re-tokenised and re-billed on every request. Cached tokens are charged at a significantly lower rate (~10% of the normal input token price after the first cache write).

### What to cache

| Payload | Rationale |
|---|---|
| System prompt (base instructions + vault index) | Entirely static within a session; the vault index changes rarely |
| Tool definitions (`read_page`, `search_vault`, `save_to_vault`) | Fixed schemas, never change at runtime |

### How to implement

Anthropic's caching API uses a `cache_control: { type: "ephemeral" }` marker on content blocks. The cache is keyed on the exact bytes of everything up to and including the marked block, so the marker must be placed at the boundary between the static prefix and the dynamic per-turn messages.

Concretely:

1. **System prompt**: Set `cache_control` on the system prompt string. In the API request body this means sending `system` as a list of content blocks rather than a plain string, with the final block carrying `"cache_control": {"type": "ephemeral"}`.

2. **Tool definitions**: Mark the last tool definition in the `tools` array with `"cache_control": {"type": "ephemeral"}`. Everything before it (all three tool schemas) is then included in the cache prefix.

Both markers can coexist in a single request; Anthropic allows up to four cache breakpoints per request.

### Cache lifetime and invalidation

Anthropic's ephemeral cache has a **5-minute TTL** that resets on each cache hit. In practice this means:

- Active conversations (turns < 5 minutes apart) will hit the cache on every turn after the first.
- The dream state goroutine, which runs nightly and submits a fresh vault snapshot, will almost always incur a cache miss and write a new cache entry.
- If the vault index is updated mid-conversation (e.g. after a `save_to_vault` call changes `index.md`), the system prompt changes and the cache entry is effectively invalidated on the next request.

### Expected savings

The system prompt + tool schemas currently total roughly 800–1,200 tokens per request. With caching active, only the first request in each 5-minute window pays the full input token price; subsequent requests in the window pay the cache-read rate. For an active chat session this reduces LLM costs by roughly 80–90% on the static prefix.

---

## 9. Build & Development Workflow

### Production Build

```makefile
build:
    cd web && npm run build
    cp -r web/build/client/* public/
    go build -o bin/autowiki ./cmd/server
```

### Development

Two processes run in parallel:
- `cd web && npm run dev` — Remix dev server with HMR
- `go run ./cmd/server --dev` — Go server; in `--dev` mode, proxies non-`/api` requests to the Remix dev server

### Configuration (`config.yaml`)

```yaml
vault_path: ~/path/to/your/obsidian/vault   # must be outside the repo; points to the live Obsidian vault on disk
server_port: 8080
anthropic_api_key: ${ANTHROPIC_API_KEY}
pebble_path: ~/.autowiki/db
auth:
  google_client_id: ${GOOGLE_CLIENT_ID}
  google_client_secret: ${GOOGLE_CLIENT_SECRET}
  allowed_email: you@gmail.com
  session_secret: ${SESSION_SECRET}          # used to sign session cookies
dream:
  enabled: true
  start_hour_utc: 19   # 19:00 UTC ≈ 1:00 AM IST
  end_hour_utc: 23     # 23:00 UTC ≈ 5:00 AM IST
```
