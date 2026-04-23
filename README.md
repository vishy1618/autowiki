# autowiki

Chat naturally while Claude silently curates a structured wiki in your Obsidian vault. Go + Remix SPA, powered by Claude Sonnet.

Inspired by [Andrej Karpathy's LLM Wiki pattern](https://x.com/karpathy/status/1761467904737067456): instead of re-reading raw sources every time you ask a question, the LLM compiles knowledge into a persistent intermediate layer — a wiki — that grows richer over time.

## What it does

- **Streaming chat** — talk to Claude naturally; it decides what's worth saving
- **Auto wiki writes** — knowledge is extracted and written to Markdown files in an Obsidian vault, with `[[wikilinks]]` between related pages
- **Attachments** — share images and PDFs; Claude describes and stores them
- **Web search & fetch** — Claude can search the web and read URLs during a conversation
- **Ask questions** — Claude searches your vault to answer from what you've already saved
- **Chat history** — infinite-scroll timeline across all past sessions
- **Nightly consolidation** — a "dream state" goroutine runs in a configurable UTC window (default 19–23 UTC ≈ 1–5 am IST) to reorganise and cross-link the vault
- **Google Drive sync** — bidirectional sync keeps your vault backed up to Drive; status pill in the header shows live sync state

## Google Drive sync

Drive sync is optional. When enabled, autowiki keeps a full copy of your Obsidian vault in Google Drive and syncs changes bidirectionally.

### How it works

- On startup, `SyncManager` uploads any local vault files not yet in Drive (initial reconcile).
- A file-system watcher (2 s debounce) uploads new or modified vault files as they change.
- A poller (default every 60 s) fetches Drive changes and downloads anything added from another device.
- Conflict resolution: if both sides changed within 5 s, behaviour depends on `DRIVE_SYNC_CONFLICT_STRATEGY`:
  - `last_write_wins` (default) — the newer file wins.
  - `keep_both` — the older version is renamed to a `.conflict.YYYYMMDDHHMMSS` file and the newer one is written.
- The header shows a live status pill (green / amber / red) with a popover for details.

### Google Cloud setup

1. Go to [Google Cloud Console](https://console.cloud.google.com) and create a project (or reuse the one for OAuth sign-in).
2. Enable the **Google Drive API** for the project.
3. Under **OAuth consent screen**, add your email as a test user (required while the app is in testing mode).
4. Under **Credentials → OAuth 2.0 Client IDs**, ensure `https://your-domain/api/auth/callback` is in the authorised redirect URIs. The same client ID and secret used for sign-in are reused — no second credential needed.

### Configuration

Set these environment variables (see `.env.example`):

```
DRIVE_SYNC_ENABLED=true
DRIVE_SYNC_ROOT_FOLDER=autowiki          # top-level Drive folder name
DRIVE_SYNC_VAULT_FOLDER=vault            # subfolder inside root for vault files
DRIVE_SYNC_POLL_INTERVAL_SECS=60         # optional, default 60
DRIVE_SYNC_CONFLICT_STRATEGY=last_write_wins  # or keep_both
```

### First sign-in

On the first sign-in after enabling Drive sync, Google shows a consent screen asking for Drive access. A refresh token is stored in Pebble and reused for all subsequent syncs — you only need to re-consent if you revoke access from your Google account settings.

If the server starts before a token exists (e.g. fresh deployment), sync begins automatically as soon as you sign in — no restart required.

## Stack

| Layer | Technology |
|---|---|
| Backend | Go |
| Frontend | React Router v7 (Remix), SPA mode |
| LLM | Claude Sonnet (`claude-sonnet-4-6`) |
| Auth | Google OAuth 2.0 + signed HTTP-only session cookies |
| Chat history | Pebble (embedded key-value store) |
| Wiki storage | Markdown files in an Obsidian vault |
| Streaming | Server-Sent Events (SSE) |

## Setup

### Prerequisites

- Go 1.22+
- Node.js 20+
- An [Anthropic API key](https://console.anthropic.com)
- A [Google OAuth 2.0 client](https://console.cloud.google.com) (for sign-in)
- An Obsidian vault directory on your machine

### Configuration

Copy `.env.example` to `.env` and fill in the values:

```
PORT=8080
VAULT_PATH=/path/to/your/obsidian/vault
PEBBLE_PATH=/path/to/pebble/data
ANTHROPIC_API_KEY=sk-ant-...
GOOGLE_CLIENT_ID=...
GOOGLE_CLIENT_SECRET=...
ALLOWED_EMAIL=you@example.com
SESSION_SECRET=<random base64 string>
```

`ALLOWED_EMAIL` is the single Google account permitted to sign in — this is a single-user, local-only tool.

### Testing

```bash
go install gotest.tools/gotestsum@latest   # one-time install
make test
```

### Build & run

```bash
make build       # builds Remix → public/, then Go binary → bin/autowiki
./bin/autowiki
```

### Development

```bash
make dev-ui      # Remix dev server with HMR on port 5173
make dev-server  # Go server in --dev mode (proxies non-/api to port 5173)
```

## Project structure

```
cmd/server/         entry point
internal/
  auth/             Google OAuth, session middleware
  chat/             SSE streaming, agentic loop
  dream/            nightly consolidation goroutine
  llm/              Claude API client
  store/            Pebble — chat history + sessions
  vault/            Obsidian vault read/write
web/                Remix SPA source
blueprint/          PRD, TRD, user stories
```
