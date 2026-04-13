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
- **US-08 through US-12**: Not started. Work through stories in order; each file in `blueprint/user-stories/` has an implementation checklist.
