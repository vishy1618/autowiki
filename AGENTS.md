# autowiki — Agent Context

autowiki is a self-maintaining personal knowledge base. A Go HTTP server + Remix SPA that lets the user have a natural chat conversation while an LLM (Claude Sonnet) silently curates a structured, interlinked wiki in an Obsidian vault.

## Key Docs

- `blueprint/PRD.md` — vision, goals, user stories index
- `blueprint/TRD.md` — architecture, component map, API spec, data models
- `blueprint/user-stories/` — one file per story (US-01 through US-11), each with acceptance criteria and an implementation checklist

## Tech Stack

| Layer | Technology |
|---|---|
| Backend | Go (`github.com/suvish/autowiki`) |
| Frontend | React Router v7 (Remix), SPA mode (`ssr: false`) |
| LLM | Claude Sonnet (`claude-sonnet-4-6`) |
| Auth | Google OAuth 2.0 + signed HTTP-only session cookies |
| Chat history | Pebble (`cockroachdb/pebble`) |
| Wiki storage | Markdown files in an Obsidian vault (path from env) |
| Streaming | Server-Sent Events (SSE) |

## Project Structure

```
cmd/server/main.go          entry point
internal/config/            config loading (YAML + env vars + .env)
internal/server/            HTTP routing, SPA fallback, dev proxy
internal/auth/              Google OAuth flow, session middleware
internal/chat/              SSE streaming, session management
internal/vault/             Obsidian vault read/write
internal/llm/               Claude API client
internal/store/             Pebble — chat history + auth sessions
internal/dream/             Nightly curation goroutine
web/                        Remix SPA source
public/                     Built Remix output (served by Go, gitignored)
blueprint/                  PRD, TRD, user stories
```

## Configuration

All config is driven by environment variables. The server loads `.env` from the working directory at startup (shell env takes precedence). Key variables:

```
PORT                    defaults to 8080
VAULT_PATH              path to the Obsidian vault (outside the repo)
PEBBLE_PATH             path to Pebble data directory
ANTHROPIC_API_KEY
GOOGLE_CLIENT_ID
GOOGLE_CLIENT_SECRET
ALLOWED_EMAIL           single whitelisted email for Google sign-in
SESSION_SECRET          random base64 string for signing cookies
```

## Build & Dev

```bash
make build        # builds Remix → public/, then Go binary → bin/autowiki
make dev-ui       # Remix dev server with HMR (port 5173)
make dev-server   # Go server in --dev mode (proxies non-/api to port 5173)
```

## Architecture Decisions Worth Knowing

- **`/api/*`** is reserved for Go handlers. Everything else is served as the Remix SPA (or proxied to the Remix dev server in `--dev` mode).
- **Sessions** are a backend-only concept (30-min inactivity boundary, stored in Pebble). The UI presents one continuous infinite-scroll chat timeline — no session concept is exposed to the user.
- **Vault writes are automatic** — the LLM judges whether each message warrants a write. It may write nothing at all (e.g. for greetings or pure queries).
- **Dream state** is a goroutine that wakes between 1–5am IST nightly to reorganise the vault. It runs at most once per night and logs changes to `log.md`.
- **The Obsidian vault lives outside the repo** at `VAULT_PATH`. `internal/vault` is Go code, not vault data.

## Commit Conventions

- Use present imperative tense: "Add login page" not "Added login page" or "Adds login page"
- First line is the subject — keep it under 72 characters
- Add a blank line followed by a brief body when the change needs context beyond what the subject conveys
- Do not end the subject line with a period

Examples:
```
Implement Google OAuth callback handler

Reject non-whitelisted emails with 403 before issuing a session token.
```
```
Fix session cookie expiry not persisting across restarts
```

## Current Status

- **US-01 — Sign in with Google**: Complete. Pebble session store, Google OAuth flow, session cookie, `/api/auth/me` probe, `/login` page, client-side auth guard.
- **US-02 — Sign out**: Complete. `POST /api/auth/logout` deletes session and clears cookie; sign-out button in home route.
- **US-03 — Basic streaming chat**: Complete. ChatStore (MemChatStore + PebbleChatStore sharing a single Pebble DB), LLM client streaming from Anthropic API, `POST /api/chat` SSE handler, chat UI with streaming bubbles.
- **US-04 — Store text knowledge in vault**: Complete. `vault.Manager` (ReadFile/WriteFile/AppendLog/ReadIndex), `save_to_vault` tool added to LLM requests with `tool_choice: auto`, chat handler parses `input_json_delta` SSE events and performs vault writes, frontend shows collapsible "Saved to vault" summary on `vault` SSE event.
- **US-05 — Share an image**: Complete. `vault.Manager` attachment support (collision-safe filenames, `.meta.json` sidecars), `llm.Client.DescribeImage` via Anthropic vision API, `internal/attachment` handler (`POST /api/attachments`), chat handler injects attachment descriptions via `attachment_ids[]`, frontend: file picker + drag-and-drop, upload chips, image thumbnails, file-type cards.
- **US-06 — Share a document**: Complete. `DescribeDocument` added to `llm.Client` (Anthropic `document` content block, base64 PDF); `Describer` interface extended; attachment handler describes PDFs ≤ 5 MB, notes oversized PDFs, leaves other types unchanged.
- **US-07 — Ask a question**: Complete. `vault.SearchPages` (case-insensitive grep walk), `read_page` and `search_vault` tool definitions in LLM client, agentic loop in chat handler (max 15 retrieval calls), `status` SSE event emitted before each retrieval tool, frontend renders transient italic status line replaced by next status/delta.
- **US-08 — Browse vault in Obsidian**: Complete. `vault.EnsureSchema` creates `schema.md` from a concise default template if absent; schema content injected into LLM system prompt as `## Wiki Schema` before the vault index; system prompt instructs LLM to use `[[wikilinks]]`, follow schema conventions, and only modify `schema.md` on explicit user request.
- **US-09 — Scroll back through chat history**: Complete. `ListSessions(limit, offset)` on ChatStore + PebbleChatStore; `GET /api/chat-sessions` and `GET /api/chat-sessions/{id}` endpoints; `formatRelative` timestamp helper; home route loads last 3 sessions on mount, renders session dividers, shows relative timestamps with absolute tooltip, and uses IntersectionObserver for infinite scroll upward.
- **US-10 — See what was stored**: Complete. Collapsible "Saved to vault" summary renders inline beneath the assistant bubble after vault writes, both live (via `vault` SSE event) and from history (via `vault_changes` field on the history API response). Each entry shows the vault-relative page path.
- **US-11 — Overnight wiki consolidation**: Complete. `vault.Manager` extended with `safePath`, `ListVault`, `ReadFilePartial`, `MoveFile`, `DeleteItem`; four new LLM tool definitions (`list_vault`, `read_page_partial`, `move_page`, `delete_item`) plus safety rule in system prompt; chat handler dispatches all seven tools with `vault` SSE events for mutations; `dream.Runner` schedules nightly 1–5 am IST runs with idempotency via `log.md` check; `dream.Consolidator` handles small vaults (≤25 pages, single LLM call) and large vaults (topology call + batch worker calls) with `StreamWithSystem` on `llm.Client`; `dreamLLMAdapter` wires it in `main.go`.
- **US-12 — Check dream state history**: Complete. Implemented as part of US-11 — dream appends a dated `## YYYY-MM-DD dream run` entry to `log.md` after each run; model reads it via `read_page` on user request.
- **US-13 — Sliding session expiry**: Complete. `Session` gains `AbsoluteExpiresAt` (30-day cap, set at login) and `GraceOnly` fields; `GetSession` rejects sessions past their absolute expiry; `RotateSession` on `SessionStore` atomically creates the new session and tombstones the old token with a 30-second grace window; `Middleware.Require` rotates when `time.Until(ExpiresAt) < 15 min` (skipping grace tokens to prevent cascade), sets new `Set-Cookie` on rotation; auth callback sets `AbsoluteExpiresAt = now + 30 days`.
- **US-15 — Search the web and fetch URLs**: Not started. `web_search_20250305` and `web_fetch_20250910` Anthropic-native tools added to `toolDefinitions`; system prompt guidance for when to use each; `status` SSE events emitted from chat handler dispatch (no Go execution — Anthropic handles server-side).
- **US-14 — Search past conversations**: Complete. `MessageSearchResult` struct + `SearchMessages(query, sessionOffset, sessionLimit)` on `ChatStore` (case-insensitive substring match, skips `tool_result` messages, 300-char snippets); implemented on both `MemChatStore` and `PebbleChatStore`; `search_chat_history` tool definition in LLM client (3 sessions/call, offset+3 to paginate, max offset 50); system prompt instructs model to use it on recall signals; chat handler dispatches tool, emits `status` SSE event, stores search results as tool result.
