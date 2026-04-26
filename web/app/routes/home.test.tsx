import { render, screen, waitFor, act } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, createMemoryRouter, RouterProvider } from "react-router";
import { describe, it, expect, vi, beforeEach } from "vitest";
import Home from "./home";

// ── helpers ──────────────────────────────────────────────────────────────────

/** Render Home inside a MemoryRouter (required for useNavigate). */
function renderHome() {
  return render(
    <MemoryRouter>
      <Home />
    </MemoryRouter>
  );
}

/**
 * Render Home with a real memory router so we can inspect navigation.
 * Returns the router instance for asserting on `router.state.location.pathname`.
 */
function renderHomeWithRouter() {
  const router = createMemoryRouter([
    { path: "/", element: <Home /> },
    { path: "/login", element: <div data-testid="login-page">Login</div> },
  ]);
  render(<RouterProvider router={router} />);
  return router;
}

/**
 * Build a ReadableStream that emits the given SSE chunks then closes.
 * Each chunk is delivered as a Uint8Array so the component's TextDecoder
 * handles it the same way a real fetch response would.
 */
function sseStream(...chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  return new ReadableStream({
    start(controller) {
      for (const chunk of chunks) {
        controller.enqueue(encoder.encode(chunk));
      }
      controller.close();
    },
  });
}

const AUTH_OK = { email: "user@example.com" };
const EMPTY_SESSIONS = { sessions: [] };

const SSE_DONE = "event: done\ndata: {\"session_id\":\"s1\"}\n\n";

type FetchEntry = Response | Promise<Response>;

/**
 * Set up a URL-routing fetch mock. Handles auth and empty sessions by default.
 * Pass overrides as { url: Response | Response[] | Promise<Response> }.
 * For the same URL called multiple times, pass an array (consumed as a queue).
 */
function mockFetch(overrides: Record<string, FetchEntry | FetchEntry[]> = {}) {
  const queues = new Map<string, FetchEntry[]>();
  for (const [url, val] of Object.entries(overrides)) {
    queues.set(url, Array.isArray(val) ? [...val] : [val]);
  }

  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = input instanceof Request ? input.url : String(input);

    // Exact-match override queue first. Check longer (more specific) patterns
    // before shorter ones so "/api/chat-sessions/s1" wins over "/api/chat-sessions".
    // A pattern matches if the URL is exactly the pattern or starts with it
    // followed by "/" or "?" (so "/api/chat" does not match "/api/chat-sessions").
    const sortedEntries = [...queues.entries()].sort((a, b) => b[0].length - a[0].length);
    for (const [pattern, queue] of sortedEntries) {
      const matches =
        url === pattern ||
        url.startsWith(pattern + "/") ||
        url.startsWith(pattern + "?");
      if (matches && queue.length > 0) {
        return queue.shift()!;
      }
    }

    // Defaults.
    if (url === "/api/auth/me")
      return new Response(JSON.stringify(AUTH_OK), { status: 200 });
    if (url.startsWith("/api/chat-sessions"))
      return new Response(JSON.stringify(EMPTY_SESSIONS), { status: 200 });

    return new Response("not found", { status: 404 });
  });
}

function chatSSE(...chunks: string[]) {
  return new Response(sseStream(...chunks), {
    status: 200,
    headers: { "Content-Type": "text/event-stream" },
  });
}

function attachResp(path: string) {
  return new Response(JSON.stringify({ path }), { status: 200 });
}

const DRIVE_DISABLED = { enabled: false, connected: false, last_vault_sync: null, last_pebble_backup: null, last_error: null };
const DRIVE_NOT_CONNECTED = { enabled: true, connected: false, last_vault_sync: null, last_pebble_backup: null, last_error: null };
const DRIVE_OK = { enabled: true, connected: true, last_vault_sync: null, last_pebble_backup: null, last_error: null };
const DRIVE_ERROR = { enabled: true, connected: true, last_vault_sync: null, last_pebble_backup: null, last_error: "quota exceeded" };

function driveResp(status: object) {
  return new Response(JSON.stringify(status), { status: 200 });
}

// ── setup ────────────────────────────────────────────────────────────────────

beforeEach(() => {
  vi.restoreAllMocks();
});

// ── tests ────────────────────────────────────────────────────────────────────

describe("Home — chat UI", () => {
  it("renders chat layout when authenticated", async () => {
    mockFetch();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    expect(screen.getByRole("button", { name: /send/i })).toBeInTheDocument();
  });

  it("redirects to /login when unauthenticated", async () => {
    mockFetch({ "/api/auth/me": [new Response("unauthorized", { status: 401 })] });
    renderHome();
    await waitFor(() =>
      expect(screen.queryByPlaceholderText(/message autowiki/i)).not.toBeInTheDocument()
    );
  });

  it("sends POST /api/chat with the typed message", async () => {
    const fetchSpy = mockFetch({ "/api/chat": [chatSSE("")] });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    await user.type(screen.getByPlaceholderText(/message autowiki/i), "hello");
    await user.click(screen.getByRole("button", { name: /send/i }));
    await waitFor(() => {
      const chatCall = fetchSpy.mock.calls.find(([url]) => url === "/api/chat");
      expect(chatCall).toBeDefined();
      expect((chatCall![1]?.body as string)).toContain("message=hello");
    });
  });

  it("renders assistant reply as markdown", async () => {
    mockFetch({
      "/api/chat": [chatSSE(
        "event: delta\ndata: {\"text\":\"**bold** and `code`\"}\n\n",
        SSE_DONE
      )],
    });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    await user.type(screen.getByPlaceholderText(/message autowiki/i), "hi");
    await user.click(screen.getByRole("button", { name: /send/i }));
    await waitFor(() =>
      expect(document.querySelector("strong")?.textContent).toBe("bold")
    );
    await waitFor(() =>
      expect(document.querySelector("code")?.textContent).toBe("code")
    );
  });

  it("re-enables the send button after stream completes", async () => {
    mockFetch({ "/api/chat": [chatSSE(SSE_DONE)] });
    const user = userEvent.setup();
    renderHome();
    const textarea = await screen.findByPlaceholderText(/message autowiki/i);
    expect(textarea).not.toBeDisabled();
    await user.type(textarea, "ping");
    await user.click(screen.getByRole("button", { name: /send/i }));
    await user.type(textarea, "next");
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /send/i })).not.toBeDisabled()
    );
  });

  it("shows an error message when the server returns non-200", async () => {
    mockFetch({ "/api/chat": [new Response("internal server error", { status: 500 })] });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    await user.type(screen.getByPlaceholderText(/message autowiki/i), "oops");
    await user.click(screen.getByRole("button", { name: /send/i }));
    await waitFor(() =>
      expect(screen.getByText(/error/i)).toBeInTheDocument()
    );
  });

  it("submits on Enter and does not submit on Shift+Enter", async () => {
    const fetchSpy = mockFetch({ "/api/chat": [chatSSE(SSE_DONE)] });
    const user = userEvent.setup();
    renderHome();
    const textarea = await screen.findByPlaceholderText(/message autowiki/i);
    await user.type(textarea, "line one{Shift>}{Enter}{/Shift}line two");
    expect(fetchSpy.mock.calls.filter(([url]) => url === "/api/chat")).toHaveLength(0);
    await user.type(textarea, "{Enter}");
    await waitFor(() =>
      expect(fetchSpy.mock.calls.filter(([url]) => url === "/api/chat")).toHaveLength(1)
    );
  });

  it("shows saved-to-vault summary when vault SSE event received", async () => {
    mockFetch({
      "/api/chat": [chatSSE(
        "event: delta\ndata: {\"text\":\"Saving that.\"}\n\n",
        "event: vault\ndata: {\"changes\":[{\"path\":\"notes/go.md\"}]}\n\n",
        SSE_DONE
      )],
    });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    await user.type(screen.getByPlaceholderText(/message autowiki/i), "save this");
    await user.click(screen.getByRole("button", { name: /send/i }));
    await waitFor(() =>
      expect(screen.getByText(/saved to vault/i)).toBeInTheDocument()
    );
    await waitFor(() =>
      expect(screen.getByText(/notes\/go\.md/)).toBeInTheDocument()
    );
  });

  it("file picker triggers POST /api/attachments upload", async () => {
    const fetchSpy = mockFetch({
      "/api/attachments": [attachResp("_attachments/photo-20260413-abc123.png")],
    });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    const input = screen.getByTestId("file-input") as HTMLInputElement;
    await user.upload(input, new File(["imgdata"], "photo.png", { type: "image/png" }));
    await waitFor(() => {
      expect(fetchSpy.mock.calls.find(([url]) => url === "/api/attachments")).toBeDefined();
    });
  });

  it("shows attachment chip after upload completes", async () => {
    mockFetch({
      "/api/attachments": [attachResp("_attachments/photo-20260413-abc123.png")],
    });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    const input = screen.getByTestId("file-input") as HTMLInputElement;
    await user.upload(input, new File(["imgdata"], "photo.png", { type: "image/png" }));
    await waitFor(() =>
      expect(screen.getByText(/photo\.png/)).toBeInTheDocument()
    );
  });

  it("sends attachment_ids with the chat message", async () => {
    const fetchSpy = mockFetch({
      "/api/attachments": [attachResp("_attachments/photo-20260413-abc123.png")],
      "/api/chat": [chatSSE(SSE_DONE)],
    });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    const input = screen.getByTestId("file-input") as HTMLInputElement;
    await user.upload(input, new File(["imgdata"], "photo.png", { type: "image/png" }));
    await waitFor(() => expect(screen.getByText(/photo\.png/)).toBeInTheDocument());
    await user.type(screen.getByPlaceholderText(/message autowiki/i), "What is this?");
    await user.click(screen.getByRole("button", { name: /send/i }));
    await waitFor(() => {
      const chatCall = fetchSpy.mock.calls.find(([url]) => url === "/api/chat");
      expect(chatCall).toBeDefined();
      const body = chatCall![1]?.body as string;
      expect(body).toContain("attachment_ids");
      expect(body).toContain("_attachments%2Fphoto");
    });
  });

  it("sends all attachment_ids when multiple files are uploaded", async () => {
    const fetchSpy = mockFetch({
      "/api/attachments": [
        attachResp("_attachments/photo1.png"),
        attachResp("_attachments/photo2.png"),
      ],
      "/api/chat": [chatSSE(SSE_DONE)],
    });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    const fileInput = screen.getByTestId("file-input") as HTMLInputElement;
    await user.upload(fileInput, [
      new File(["data1"], "photo1.png", { type: "image/png" }),
      new File(["data2"], "photo2.png", { type: "image/png" }),
    ]);
    await waitFor(() => expect(screen.getByText(/photo1\.png/)).toBeInTheDocument());
    await waitFor(() => expect(screen.getByText(/photo2\.png/)).toBeInTheDocument());
    await user.type(screen.getByPlaceholderText(/message autowiki/i), "What are these?");
    await user.click(screen.getByRole("button", { name: /send/i }));
    await waitFor(() => {
      const chatCall = fetchSpy.mock.calls.find(([url]) => url === "/api/chat");
      expect(chatCall).toBeDefined();
      const body = chatCall![1]?.body as string;
      expect(body).toContain("photo1");
      expect(body).toContain("photo2");
    });
  });

  it("shows all attachments in the sent message bubble", async () => {
    mockFetch({
      "/api/attachments": [
        attachResp("_attachments/photo1.png"),
        attachResp("_attachments/photo2.png"),
      ],
      "/api/chat": [chatSSE(SSE_DONE)],
    });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    const fileInput = screen.getByTestId("file-input") as HTMLInputElement;
    await user.upload(fileInput, [
      new File(["data1"], "photo1.png", { type: "image/png" }),
      new File(["data2"], "photo2.png", { type: "image/png" }),
    ]);
    await waitFor(() => expect(screen.getByText(/photo1\.png/)).toBeInTheDocument());
    await waitFor(() => expect(screen.getByText(/photo2\.png/)).toBeInTheDocument());
    await user.type(screen.getByPlaceholderText(/message autowiki/i), "What are these?");
    await user.click(screen.getByRole("button", { name: /send/i }));
    await waitFor(() => {
      const alts = Array.from(document.querySelectorAll("img[alt]")).map(
        (img) => img.getAttribute("alt")
      );
      expect(alts).toContain("photo1.png");
      expect(alts).toContain("photo2.png");
    });
  });

  it("sends all attachment_ids when files are added one at a time", async () => {
    const fetchSpy = mockFetch({
      "/api/attachments": [
        attachResp("_attachments/photo1.png"),
        attachResp("_attachments/photo2.png"),
      ],
      "/api/chat": [chatSSE(SSE_DONE)],
    });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    const fileInput = screen.getByTestId("file-input") as HTMLInputElement;
    await user.upload(fileInput, new File(["data1"], "photo1.png", { type: "image/png" }));
    await user.upload(fileInput, new File(["data2"], "photo2.png", { type: "image/png" }));
    await waitFor(() => expect(screen.getByText(/photo1\.png/)).toBeInTheDocument());
    await waitFor(() => expect(screen.getByText(/photo2\.png/)).toBeInTheDocument());
    await user.type(screen.getByPlaceholderText(/message autowiki/i), "What are these?");
    await user.click(screen.getByRole("button", { name: /send/i }));
    await waitFor(() => {
      const chatCall = fetchSpy.mock.calls.find(([url]) => url === "/api/chat");
      expect(chatCall).toBeDefined();
      const body = chatCall![1]?.body as string;
      expect(body).toContain("photo1");
      expect(body).toContain("photo2");
    });
  });

  it("disables send button while an attachment is uploading", async () => {
    let resolveUpload!: (r: Response) => void;
    const uploadPending = new Promise<Response>((res) => { resolveUpload = res; });
    mockFetch({ "/api/attachments": [uploadPending] });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    const fileInput = screen.getByTestId("file-input") as HTMLInputElement;
    await user.upload(fileInput, new File(["data"], "photo.png", { type: "image/png" }));
    await user.type(screen.getByPlaceholderText(/message autowiki/i), "hello");
    expect(screen.getByRole("button", { name: /send/i })).toBeDisabled();
    resolveUpload(new Response(JSON.stringify({ path: "_attachments/photo.png" }), { status: 200 }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /send/i })).not.toBeDisabled()
    );
  });

  it("shows a spinner (not an arrow) while attachment is uploading", async () => {
    let resolveUpload!: (r: Response) => void;
    const uploadPending = new Promise<Response>((res) => { resolveUpload = res; });
    mockFetch({ "/api/attachments": [uploadPending] });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    const fileInput = screen.getByTestId("file-input") as HTMLInputElement;
    await user.upload(fileInput, new File(["data"], "photo.png", { type: "image/png" }));
    expect(screen.getByRole("progressbar")).toBeInTheDocument();
    expect(screen.queryByText("↑")).not.toBeInTheDocument();
    resolveUpload(new Response(JSON.stringify({ path: "_attachments/photo.png" }), { status: 200 }));
    await waitFor(() => expect(screen.queryByRole("progressbar")).not.toBeInTheDocument());
  });

  it("does not send on Enter while an attachment is uploading", async () => {
    let resolveUpload!: (r: Response) => void;
    const uploadPending = new Promise<Response>((res) => { resolveUpload = res; });
    const fetchSpy = mockFetch({ "/api/attachments": [uploadPending] });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    const fileInput = screen.getByTestId("file-input") as HTMLInputElement;
    await user.upload(fileInput, new File(["data"], "photo.png", { type: "image/png" }));
    await user.type(screen.getByPlaceholderText(/message autowiki/i), "hello{Enter}");
    expect(fetchSpy.mock.calls.filter(([url]) => url === "/api/chat")).toHaveLength(0);
    resolveUpload(new Response(JSON.stringify({ path: "_attachments/photo.png" }), { status: 200 }));
  });

  /**
   * SSE response that pauses mid-stream. `firstChunks` are enqueued right
   * away; the stream blocks until `resume()` is called, then `secondChunks`
   * are enqueued and the stream closes. This lets tests assert on transient
   * DOM state that React renders between the two batches.
   */
  function pausedChatSSE(firstChunks: string[], secondChunks: string[]) {
    const encoder = new TextEncoder();
    let resume!: () => void;
    const gate = new Promise<void>((r) => { resume = r; });
    const stream = new ReadableStream<Uint8Array>({
      async start(controller) {
        for (const c of firstChunks) controller.enqueue(encoder.encode(c));
        await gate;
        for (const c of secondChunks) controller.enqueue(encoder.encode(c));
        controller.close();
      },
    });
    return {
      response: new Response(stream, {
        status: 200,
        headers: { "Content-Type": "text/event-stream" },
      }),
      resume,
    };
  }

  it("shows status message during streaming", async () => {
    const { response, resume } = pausedChatSSE(
      ["event: status\ndata: {\"message\":\"Reading programming/go.md\u2026\"}\n\n"],
      [SSE_DONE]
    );
    mockFetch({ "/api/chat": [response] });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    await user.type(screen.getByPlaceholderText(/message autowiki/i), "tell me about Go");
    await user.click(screen.getByRole("button", { name: /send/i }));
    // Stream is paused — React has rendered the status-only state.
    await waitFor(() =>
      expect(screen.getByText(/Reading programming\/go\.md/)).toBeInTheDocument()
    );
    act(() => resume());
  });

  it("status message is replaced by the next one", async () => {
    const { response, resume } = pausedChatSSE(
      [
        "event: status\ndata: {\"message\":\"Reading first.md\u2026\"}\n\n",
        "event: status\ndata: {\"message\":\"Reading second.md\u2026\"}\n\n",
      ],
      [SSE_DONE]
    );
    mockFetch({ "/api/chat": [response] });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    await user.type(screen.getByPlaceholderText(/message autowiki/i), "tell me about Go");
    await user.click(screen.getByRole("button", { name: /send/i }));
    // Stream is paused — both status events have been processed.
    await waitFor(() => {
      expect(screen.getByText(/Reading second\.md/)).toBeInTheDocument();
      expect(screen.queryByText(/Reading first\.md/)).not.toBeInTheDocument();
    });
    act(() => resume());
  });

  it("redirects to /login when /api/chat returns 401", async () => {
    mockFetch({ "/api/chat": [new Response("Unauthorized", { status: 401 })] });
    const user = userEvent.setup();
    const router = renderHomeWithRouter();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    await user.type(screen.getByPlaceholderText(/message autowiki/i), "hello");
    await user.click(screen.getByRole("button", { name: /send/i }));
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/login")
    );
  });

  it("redirects to /login when /api/attachments returns 401", async () => {
    mockFetch({ "/api/attachments": [new Response("Unauthorized", { status: 401 })] });
    const user = userEvent.setup();
    const router = renderHomeWithRouter();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    const fileInput = screen.getByTestId("file-input") as HTMLInputElement;
    await user.upload(fileInput, new File(["data"], "doc.pdf", { type: "application/pdf" }));
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/login")
    );
  });

  it("keeps assistant bubble and shows vault summary when LLM saves without producing text", async () => {
    // When the LLM calls save_to_vault without emitting any text, the stream
    // delivers only a vault event (no delta). The finally cleanup must NOT pop
    // the bubble — the vault summary is still meaningful to the user.
    mockFetch({
      "/api/chat": [chatSSE(
        "event: vault\ndata: {\"changes\":[{\"path\":\"notes/silent.md\"}]}\n\n",
        SSE_DONE
      )],
    });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    await user.type(screen.getByPlaceholderText(/message autowiki/i), "save this PDF");
    await user.click(screen.getByRole("button", { name: /send/i }));
    await waitFor(() =>
      expect(screen.getByText(/saved to vault/i)).toBeInTheDocument()
    );
    expect(screen.getByText(/notes\/silent\.md/)).toBeInTheDocument();
  });

  it("does not show vault summary when no vault event", async () => {
    mockFetch({
      "/api/chat": [chatSSE(
        "event: delta\ndata: {\"text\":\"Just chatting.\"}\n\n",
        SSE_DONE
      )],
    });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    await user.type(screen.getByPlaceholderText(/message autowiki/i), "hello");
    await user.click(screen.getByRole("button", { name: /send/i }));
    await waitFor(() =>
      expect(screen.getByText(/Just chatting/)).toBeInTheDocument()
    );
    expect(screen.queryByText(/saved to vault/i)).not.toBeInTheDocument();
  });

  // ── Group 4: rendering of loaded history ────────────────────────────────────

  it("renders timestamps on messages loaded from history", async () => {
    // Use a far-past created_at so formatRelative returns a date string (e.g. "10 Apr")
    // regardless of when the test runs — no fake timers needed.
    mockFetch({
      "/api/chat-sessions": [
        new Response(
          JSON.stringify({
            sessions: [{ id: "s1", created_at: "2026-04-10T08:00:00Z", last_active_at: "2026-04-10T08:01:00Z" }],
          }),
          { status: 200 }
        ),
      ],
      "/api/chat-sessions/s1": [
        new Response(
          JSON.stringify({ messages: [{ id: "m1", role: "user", content: "hello history", created_at: "2026-04-10T08:00:00Z" }] }),
          { status: 200 }
        ),
      ],
    });

    renderHome();

    await waitFor(() => expect(screen.getByText("hello history")).toBeInTheDocument());
    // A <span title="..."> should be rendered showing the relative timestamp.
    const timestampEl = document.querySelector("span[title]");
    expect(timestampEl).not.toBeNull();
    // Title holds the absolute datetime (locale string, e.g. "4/10/2026, ...").
    expect(timestampEl?.getAttribute("title")).toMatch(/2026/);
  });

  // ── Group 4 (continued): infinite scroll ────────────────────────────────────

  /** Set up a fake IntersectionObserver and return a function to trigger it. */
  function mockIntersectionObserver() {
    let storedCallback: IntersectionObserverCallback | null = null;
    const MockIO = class {
      constructor(cb: IntersectionObserverCallback) {
        storedCallback = cb;
      }
      observe() {}
      unobserve() {}
      disconnect() {}
    };
    vi.stubGlobal("IntersectionObserver", MockIO);
    return function triggerVisible() {
      storedCallback?.([{ isIntersecting: true } as IntersectionObserverEntry], {} as IntersectionObserver);
    };
  }

  it("prepends older sessions when sentinel scrolls into view", async () => {
    const triggerSentinel = mockIntersectionObserver();

    mockFetch({
      "/api/chat-sessions": [
        // First call (offset=0): returns 3 sessions (limit reached → more may exist)
        new Response(
          JSON.stringify({
            sessions: [
              { id: "s3", created_at: "2026-04-14T11:00:00Z", last_active_at: "2026-04-14T11:05:00Z" },
              { id: "s2", created_at: "2026-04-14T10:00:00Z", last_active_at: "2026-04-14T10:05:00Z" },
              { id: "s1", created_at: "2026-04-14T09:00:00Z", last_active_at: "2026-04-14T09:05:00Z" },
            ],
          }),
          { status: 200 }
        ),
        // Second call (offset=3): returns the older session
        new Response(
          JSON.stringify({
            sessions: [
              { id: "s0", created_at: "2026-04-14T08:00:00Z", last_active_at: "2026-04-14T08:05:00Z" },
            ],
          }),
          { status: 200 }
        ),
      ],
      "/api/chat-sessions/s3": [
        new Response(JSON.stringify({ messages: [{ id: "m3", role: "user", content: "newest", created_at: "2026-04-14T11:00:00Z" }] }), { status: 200 }),
      ],
      "/api/chat-sessions/s2": [
        new Response(JSON.stringify({ messages: [{ id: "m2", role: "user", content: "middle", created_at: "2026-04-14T10:00:00Z" }] }), { status: 200 }),
      ],
      "/api/chat-sessions/s1": [
        new Response(JSON.stringify({ messages: [{ id: "m1", role: "user", content: "older", created_at: "2026-04-14T09:00:00Z" }] }), { status: 200 }),
      ],
      "/api/chat-sessions/s0": [
        new Response(JSON.stringify({ messages: [{ id: "m0", role: "user", content: "oldest", created_at: "2026-04-14T08:00:00Z" }] }), { status: 200 }),
      ],
    });

    renderHome();

    // Wait for initial 3 sessions to load.
    await waitFor(() => expect(screen.getByText("newest")).toBeInTheDocument());

    // Trigger the sentinel (simulate scroll to top).
    act(() => triggerSentinel());

    // Older session message should now appear.
    await waitFor(() => expect(screen.getByText("oldest")).toBeInTheDocument());
  });

  it("inserts a newline before first delta after a status event", async () => {
    mockFetch({
      "/api/chat": [chatSSE(
        "event: delta\ndata: {\"text\":\"Let me search for it.\"}\n\n",
        "event: status\ndata: {\"message\":\"Searching vault\u2026\"}\n\n",
        "event: delta\ndata: {\"text\":\"I found it.\"}\n\n",
        SSE_DONE
      )],
    });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    await user.type(screen.getByPlaceholderText(/message autowiki/i), "find it");
    await user.click(screen.getByRole("button", { name: /send/i }));
    await waitFor(() =>
      expect(screen.getByText(/I found it/)).toBeInTheDocument()
    );
    // The content should have a newline separating pre-tool and post-tool text.
    // react-markdown renders "\n\n" as a paragraph break, so the two sentences
    // must appear in distinct <p> elements — no single paragraph contains both.
    const paragraphs = Array.from(document.querySelectorAll(".md p")).map(
      (p) => p.textContent ?? ""
    );
    const hasBoth = paragraphs.some(
      (t) => /Let me search for it/.test(t) && /I found it/.test(t)
    );
    expect(hasBoth).toBe(false);
    expect(paragraphs.some((t) => /Let me search for it/.test(t))).toBe(true);
    expect(paragraphs.some((t) => /I found it/.test(t))).toBe(true);
  });

  it("polls /api/drive/status on mount", async () => {
    const fetchSpy = mockFetch({ "/api/drive/status": [driveResp(DRIVE_DISABLED)] });
    renderHome();
    await waitFor(() => expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument());
    await waitFor(() =>
      expect(fetchSpy.mock.calls.some(([url]) => String(url) === "/api/drive/status")).toBe(true)
    );
  });

  it("stops fetching when fewer sessions than limit are returned", async () => {
    const triggerSentinel = mockIntersectionObserver();
    const fetchSpy = mockFetch({
      "/api/chat-sessions": [
        // Only 2 sessions returned (< limit of 3 → no more history)
        new Response(
          JSON.stringify({
            sessions: [
              { id: "s2", created_at: "2026-04-14T10:00:00Z", last_active_at: "2026-04-14T10:05:00Z" },
              { id: "s1", created_at: "2026-04-14T09:00:00Z", last_active_at: "2026-04-14T09:05:00Z" },
            ],
          }),
          { status: 200 }
        ),
      ],
      "/api/chat-sessions/s2": [
        new Response(JSON.stringify({ messages: [{ id: "m2", role: "user", content: "b", created_at: "2026-04-14T10:00:00Z" }] }), { status: 200 }),
      ],
      "/api/chat-sessions/s1": [
        new Response(JSON.stringify({ messages: [{ id: "m1", role: "user", content: "a", created_at: "2026-04-14T09:00:00Z" }] }), { status: 200 }),
      ],
    });

    renderHome();

    await waitFor(() => expect(screen.getByText("a")).toBeInTheDocument());

    const callsBeforeTrigger = fetchSpy.mock.calls.filter(([url]) =>
      String(url).startsWith("/api/chat-sessions")
    ).length;

    // Trigger sentinel — should NOT cause another fetch since limit wasn't reached.
    act(() => triggerSentinel());

    await new Promise((r) => setTimeout(r, 50));

    const callsAfterTrigger = fetchSpy.mock.calls.filter(([url]) =>
      String(url).startsWith("/api/chat-sessions")
    ).length;

    expect(callsAfterTrigger).toBe(callsBeforeTrigger);
  });
});

describe("Home — DriveSyncStatus pill", () => {
  it("renders no pill when drive sync is disabled", async () => {
    mockFetch({ "/api/drive/status": [driveResp(DRIVE_DISABLED)] });
    renderHome();
    await waitFor(() => expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument());
    expect(screen.queryByText(/not connected/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/sync error/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/synced/i)).not.toBeInTheDocument();
  });

  it("shows 'Not connected' pill when enabled but not connected", async () => {
    mockFetch({ "/api/drive/status": [driveResp(DRIVE_NOT_CONNECTED)] });
    renderHome();
    await waitFor(() => expect(screen.getByText(/not connected/i)).toBeInTheDocument());
  });

  it("shows 'Sync error' pill when connected with error", async () => {
    mockFetch({ "/api/drive/status": [driveResp(DRIVE_ERROR)] });
    renderHome();
    await waitFor(() => expect(screen.getByText(/sync error/i)).toBeInTheDocument());
  });

  it("shows 'Synced' pill when connected with no error and no last sync", async () => {
    mockFetch({ "/api/drive/status": [driveResp(DRIVE_OK)] });
    renderHome();
    await waitFor(() => expect(screen.getByText(/^synced$/i)).toBeInTheDocument());
  });

  it("shows relative time in pill when last_vault_sync is set", async () => {
    const ts = new Date(Date.now() - 3 * 60 * 1000).toISOString();
    mockFetch({ "/api/drive/status": [driveResp({ ...DRIVE_OK, last_vault_sync: ts })] });
    renderHome();
    await waitFor(() => expect(screen.getByText(/synced.*minutes ago/i)).toBeInTheDocument());
  });

  it("opens popover with not-connected message on pill click", async () => {
    mockFetch({ "/api/drive/status": [driveResp(DRIVE_NOT_CONNECTED)] });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() => expect(screen.getByText(/not connected/i)).toBeInTheDocument());
    await user.click(screen.getByText(/not connected/i));
    expect(screen.getByText(/sign out and back in/i)).toBeInTheDocument();
  });

  it("opens popover with error details on pill click", async () => {
    mockFetch({ "/api/drive/status": [driveResp(DRIVE_ERROR)] });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() => expect(screen.getByText(/sync error/i)).toBeInTheDocument());
    await user.click(screen.getByText(/sync error/i));
    expect(screen.getByText(/quota exceeded/)).toBeInTheDocument();
    expect(screen.getByText(/check server logs/i)).toBeInTheDocument();
  });

  it("opens popover with last sync time when connected and ok", async () => {
    const ts = "2026-04-20T10:30:00Z";
    mockFetch({ "/api/drive/status": [driveResp({ ...DRIVE_OK, last_vault_sync: ts })] });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() => expect(screen.getByText(/synced/i)).toBeInTheDocument());
    await user.click(screen.getByText(/synced/i));
    expect(screen.getByText(/last synced:/i)).toBeInTheDocument();
    expect(screen.getByText(/2026/)).toBeInTheDocument();
  });

  it("opens popover with 'No syncs recorded yet' when connected but no last sync", async () => {
    mockFetch({ "/api/drive/status": [driveResp(DRIVE_OK)] });
    const user = userEvent.setup();
    renderHome();
    await waitFor(() => expect(screen.getByText(/^synced$/i)).toBeInTheDocument());
    await user.click(screen.getByText(/^synced$/i));
    expect(screen.getByText(/no syncs recorded yet/i)).toBeInTheDocument();
  });
});


