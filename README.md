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
- **Nightly consolidation** — a "dream state" goroutine runs between 1–5 am to reorganise and cross-link the vault

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
