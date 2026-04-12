import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
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

// ── setup ────────────────────────────────────────────────────────────────────

beforeEach(() => {
  vi.restoreAllMocks();
});

// ── tests ────────────────────────────────────────────────────────────────────

describe("Home — chat UI", () => {
  it("renders chat layout when authenticated", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );

    renderHome();

    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );
    expect(screen.getByRole("button", { name: /send/i })).toBeInTheDocument();
  });

  it("redirects to /login when unauthenticated", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response("unauthorized", { status: 401 })
    );

    renderHome();

    // Component renders null then navigates — it should not show the input.
    await waitFor(() =>
      expect(
        screen.queryByPlaceholderText(/message autowiki/i)
      ).not.toBeInTheDocument()
    );
  });

  it("sends POST /api/chat with the typed message", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    // First call: auth probe.
    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    // Second call: chat endpoint — returns empty SSE stream.
    fetchSpy.mockResolvedValueOnce(
      new Response(sseStream(""), {
        status: 200,
        headers: { "Content-Type": "text/event-stream" },
      })
    );

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
      const [, init] = chatCall!;
      expect((init?.body as string)).toContain("message=hello");
    });
  });

  it("renders assistant reply as markdown", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    fetchSpy.mockResolvedValueOnce(
      new Response(
        sseStream(
          "event: delta\ndata: {\"text\":\"**bold** and `code`\"}\n\n",
          "event: done\ndata: {\"session_id\":\"s1\"}\n\n"
        ),
        { status: 200, headers: { "Content-Type": "text/event-stream" } }
      )
    );

    const user = userEvent.setup();
    renderHome();

    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );

    await user.type(screen.getByPlaceholderText(/message autowiki/i), "hi");
    await user.click(screen.getByRole("button", { name: /send/i }));

    // Should render a <strong> for **bold**, not the literal asterisks.
    await waitFor(() =>
      expect(screen.getByRole("strong") ?? document.querySelector("strong")).toBeTruthy()
    );
    await waitFor(() =>
      expect(document.querySelector("strong")?.textContent).toBe("bold")
    );
    // Should render a <code> for backtick code.
    await waitFor(() =>
      expect(document.querySelector("code")?.textContent).toBe("code")
    );
  });

  it("streams delta text into the assistant bubble", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    fetchSpy.mockResolvedValueOnce(
      new Response(
        sseStream(
          "event: delta\ndata: {\"text\":\"Hello\"}\n\n",
          "event: delta\ndata: {\"text\":\" world\"}\n\n",
          "event: done\ndata: {\"session_id\":\"s1\"}\n\n"
        ),
        { status: 200, headers: { "Content-Type": "text/event-stream" } }
      )
    );

    const user = userEvent.setup();
    renderHome();

    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );

    await user.type(screen.getByPlaceholderText(/message autowiki/i), "hi");
    await user.click(screen.getByRole("button", { name: /send/i }));

    await waitFor(() =>
      expect(screen.getByText(/Hello world/)).toBeInTheDocument()
    );
  });

  it("re-enables the send button after stream completes", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    fetchSpy.mockResolvedValueOnce(
      new Response(
        sseStream("event: done\ndata: {\"session_id\":\"s1\"}\n\n"),
        { status: 200, headers: { "Content-Type": "text/event-stream" } }
      )
    );

    const user = userEvent.setup();
    renderHome();

    const textarea = await screen.findByPlaceholderText(/message autowiki/i);
    // Textarea is never disabled, so the user can always type.
    expect(textarea).not.toBeDisabled();

    await user.type(textarea, "ping");
    await user.click(screen.getByRole("button", { name: /send/i }));

    // Send button re-enables once input is non-empty and stream has finished.
    await user.type(textarea, "next");
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /send/i })).not.toBeDisabled()
    );
  });

  it("shows an error message when the server returns non-200", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    fetchSpy.mockResolvedValueOnce(
      new Response("internal server error", { status: 500 })
    );

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
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    // Only one chat call should happen (for the Enter press).
    fetchSpy.mockResolvedValue(
      new Response(
        sseStream("event: done\ndata: {\"session_id\":\"s1\"}\n\n"),
        { status: 200, headers: { "Content-Type": "text/event-stream" } }
      )
    );

    const user = userEvent.setup();
    renderHome();

    const textarea = await screen.findByPlaceholderText(/message autowiki/i);

    // Shift+Enter should add a newline, not submit.
    await user.type(textarea, "line one{Shift>}{Enter}{/Shift}line two");
    const chatCallsBefore = fetchSpy.mock.calls.filter(
      ([url]) => url === "/api/chat"
    ).length;
    expect(chatCallsBefore).toBe(0);

    // Plain Enter should submit.
    await user.type(textarea, "{Enter}");
    await waitFor(() => {
      const chatCalls = fetchSpy.mock.calls.filter(
        ([url]) => url === "/api/chat"
      );
      expect(chatCalls.length).toBe(1);
    });
  });
});
