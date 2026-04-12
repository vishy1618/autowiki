# autowiki — Product Requirements Document

## 1. Vision

autowiki is a self-maintaining personal knowledge base. It is a locally-run service that lets you have a natural conversation — sharing articles, images, documents, or raw thoughts — while an LLM silently curates a structured, interlinked wiki in an Obsidian vault on your behalf.

The core insight (from Andrej Karpathy's LLM Wiki pattern): instead of re-reading raw sources every time you ask a question, the LLM compiles knowledge into a persistent "intermediate layer" — a wiki — that grows richer over time. Cross-references, synthesis, and summaries are built once and reused forever.

The human curates sources and asks questions. The LLM handles the bookkeeping.

---

## 2. Goals

- Provide a streaming chat interface for sharing and querying personal knowledge.
- Automatically ingest text, images, and documents into a structured Obsidian vault.
- Maintain a human-readable, well-linked wiki that looks like something a thoughtful person built manually.
- Persist full chat history locally.
- Run a nightly background process ("dream state") to consolidate and reorganize the wiki.
- Keep the entire system local and single-instance.

## 3. Non-Goals

- No multi-user support — a single whitelisted email address may access the service.
- No cloud sync or remote hosting.
- No custom vector database or embedding search.
- No mobile app.
- No manual vault write triggers — all writes are automated and LLM-judged.

---

## 4. User Stories

Each story follows the INVEST principle: Independent, Negotiable, Valuable, Estimable, Small, Testable. Full stories with acceptance criteria live in [`blueprint/user-stories/`](./user-stories/).

| ID | Title | Area |
|---|---|---|
| [US-01](./user-stories/US-01.md) | Sign in with Google | Authentication |
| [US-02](./user-stories/US-02.md) | Sign out | Authentication |
| [US-03](./user-stories/US-03.md) | Share text knowledge | Knowledge Capture |
| [US-04](./user-stories/US-04.md) | Share an image | Knowledge Capture |
| [US-05](./user-stories/US-05.md) | Share a document | Knowledge Capture |
| [US-06](./user-stories/US-06.md) | Ask a question | Knowledge Retrieval |
| [US-07](./user-stories/US-07.md) | Browse vault in Obsidian | Knowledge Retrieval |
| [US-08](./user-stories/US-08.md) | Scroll back through chat history | Chat History |
| [US-09](./user-stories/US-09.md) | See what was stored | Vault Maintenance |
| [US-10](./user-stories/US-10.md) | Overnight wiki consolidation | Vault Maintenance |
| [US-11](./user-stories/US-11.md) | Check dream state history | Vault Maintenance |

---

## 5. Vault Structure

The vault is designed to look like something a thoughtful person built manually. Folder structure emerges organically from content — the LLM creates folders as warranted, not from a fixed schema.

```
vault/
├── index.md              ← Map of Content: living table of contents
├── log.md                ← Append-only: what was added/changed/when
├── schema.md             ← Wiki conventions (fed to LLM as system context)
├── _attachments/         ← Raw uploaded files (images, PDFs, docs)
│
├── Topics/               ← Concept and idea pages
├── People/               ← Person pages
├── Sources/              ← Papers, articles, books
└── Projects/             ← Ongoing work (created as needed)
```

### Page Conventions

Each wiki page follows this structure:
1. Short prose summary (2–4 sentences) at the top.
2. Growing sections with details, observations, and analysis.
3. Inline `[[wikilinks]]` to related pages throughout.
4. YAML frontmatter with `tags`.
5. `## Sources` section at the bottom linking back to origin.

### `index.md`

A Map of Content (MOC) listing all pages by category with one-line descriptions. Updated by the LLM on every ingest and during dream state.

### `log.md`

Append-only. Each entry records what was created or updated, and the source of the information.

---

## 6. LLM Write Judgment

The LLM behaves as a **wiki curator with good judgment**. It will choose not to write to the vault when:
- The message is a query only (no new information shared).
- The information is already present verbatim or substantively in the vault.
- The message is conversational with no knowledge value (e.g., "thanks", "ok").
- The information is too ephemeral or unverified to be worth storing.

When writes are performed, the LLM produces minimal updates — changing only what needs to change, not rewriting entire pages.

---

## 7. Dream State

A nightly background process that runs while the user is asleep. It acts as a second pass of curation — not ingesting new information, but improving what is already there:

- Merging or splitting pages where warranted.
- Adding missing cross-references between related pages.
- Fixing broken links and orphan pages.
- Reorganizing folder structure if it has become inconsistent.
- Refreshing `index.md` to reflect the current vault state.

A summary of what was changed is appended to `log.md` each night it runs.

---

## 8. Out of Scope (v1)

- Multiple vault support
- Plugin system
- Mobile interface
- Export / backup tooling
- Semantic/vector search
