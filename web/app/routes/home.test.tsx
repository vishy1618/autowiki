import { render, screen, waitFor } from "@testing-library/react";
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

  it("shows saved-to-vault summary when vault SSE event received", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    fetchSpy.mockResolvedValueOnce(
      new Response(
        sseStream(
          "event: delta\ndata: {\"text\":\"Saving that.\"}\n\n",
          "event: vault\ndata: {\"changes\":[{\"path\":\"notes/go.md\"}]}\n\n",
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
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    fetchSpy.mockResolvedValueOnce(
      new Response(
        JSON.stringify({ id: "att_photo", path: "_attachments/photo-20260413-abc123.png", description: "a sunset" }),
        { status: 200 }
      )
    );

    const user = userEvent.setup();
    renderHome();

    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );

    const input = screen.getByTestId("file-input") as HTMLInputElement;
    const file = new File(["imgdata"], "photo.png", { type: "image/png" });
    await user.upload(input, file);

    await waitFor(() => {
      const uploadCall = fetchSpy.mock.calls.find(([url]) => url === "/api/attachments");
      expect(uploadCall).toBeDefined();
    });
  });

  it("shows attachment chip after upload completes", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    fetchSpy.mockResolvedValueOnce(
      new Response(
        JSON.stringify({ id: "att_photo", path: "_attachments/photo-20260413-abc123.png", description: "a sunset" }),
        { status: 200 }
      )
    );

    const user = userEvent.setup();
    renderHome();

    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );

    const input = screen.getByTestId("file-input") as HTMLInputElement;
    const file = new File(["imgdata"], "photo.png", { type: "image/png" });
    await user.upload(input, file);

    await waitFor(() =>
      expect(screen.getByText(/photo\.png/)).toBeInTheDocument()
    );
  });

  it("sends attachment_ids with the chat message", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    // Upload response
    fetchSpy.mockResolvedValueOnce(
      new Response(
        JSON.stringify({ id: "att_photo", path: "_attachments/photo-20260413-abc123.png", description: "a sunset" }),
        { status: 200 }
      )
    );
    // Chat response
    fetchSpy.mockResolvedValueOnce(
      new Response(
        sseStream("event: done\ndata: {\"session_id\":\"s1\"}\n\n"),
        { status: 200, headers: { "Content-Type": "text/event-stream" } }
      )
    );

    const user = userEvent.setup();
    renderHome();

    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );

    // Upload a file
    const input = screen.getByTestId("file-input") as HTMLInputElement;
    const file = new File(["imgdata"], "photo.png", { type: "image/png" });
    await user.upload(input, file);
    await waitFor(() => expect(screen.getByText(/photo\.png/)).toBeInTheDocument());

    // Send a message
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
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    // Upload responses for two files.
    fetchSpy.mockResolvedValueOnce(
      new Response(
        JSON.stringify({ id: "att_1", path: "_attachments/photo1.png", description: "" }),
        { status: 200 }
      )
    );
    fetchSpy.mockResolvedValueOnce(
      new Response(
        JSON.stringify({ id: "att_2", path: "_attachments/photo2.png", description: "" }),
        { status: 200 }
      )
    );
    // Chat response.
    fetchSpy.mockResolvedValueOnce(
      new Response(
        sseStream("event: done\ndata: {\"session_id\":\"s1\"}\n\n"),
        { status: 200, headers: { "Content-Type": "text/event-stream" } }
      )
    );

    const user = userEvent.setup();
    renderHome();

    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );

    const fileInput = screen.getByTestId("file-input") as HTMLInputElement;
    const file1 = new File(["data1"], "photo1.png", { type: "image/png" });
    const file2 = new File(["data2"], "photo2.png", { type: "image/png" });
    await user.upload(fileInput, [file1, file2]);

    // Both chips must appear before sending.
    await waitFor(() => expect(screen.getByText(/photo1\.png/)).toBeInTheDocument());
    await waitFor(() => expect(screen.getByText(/photo2\.png/)).toBeInTheDocument());

    await user.type(screen.getByPlaceholderText(/message autowiki/i), "What are these?");
    await user.click(screen.getByRole("button", { name: /send/i }));

    // Both attachment paths must be present in the chat request body.
    await waitFor(() => {
      const chatCall = fetchSpy.mock.calls.find(([url]) => url === "/api/chat");
      expect(chatCall).toBeDefined();
      const body = chatCall![1]?.body as string;
      expect(body).toContain("photo1");
      expect(body).toContain("photo2");
    });
  });

  it("shows all attachments in the sent message bubble", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    fetchSpy.mockResolvedValueOnce(
      new Response(
        JSON.stringify({ id: "att_1", path: "_attachments/photo1.png", description: "" }),
        { status: 200 }
      )
    );
    fetchSpy.mockResolvedValueOnce(
      new Response(
        JSON.stringify({ id: "att_2", path: "_attachments/photo2.png", description: "" }),
        { status: 200 }
      )
    );
    fetchSpy.mockResolvedValueOnce(
      new Response(
        sseStream("event: done\ndata: {\"session_id\":\"s1\"}\n\n"),
        { status: 200, headers: { "Content-Type": "text/event-stream" } }
      )
    );

    const user = userEvent.setup();
    renderHome();

    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );

    const fileInput = screen.getByTestId("file-input") as HTMLInputElement;
    const file1 = new File(["data1"], "photo1.png", { type: "image/png" });
    const file2 = new File(["data2"], "photo2.png", { type: "image/png" });
    await user.upload(fileInput, [file1, file2]);

    await waitFor(() => expect(screen.getByText(/photo1\.png/)).toBeInTheDocument());
    await waitFor(() => expect(screen.getByText(/photo2\.png/)).toBeInTheDocument());

    await user.type(screen.getByPlaceholderText(/message autowiki/i), "What are these?");
    await user.click(screen.getByRole("button", { name: /send/i }));

    // After send, both attachments must appear in the user bubble.
    await waitFor(() => {
      const imgs = document.querySelectorAll("img[alt]");
      const alts = Array.from(imgs).map((img) => img.getAttribute("alt"));
      expect(alts).toContain("photo1.png");
      expect(alts).toContain("photo2.png");
    });
  });

  it("sends all attachment_ids when files are added one at a time", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    fetchSpy.mockResolvedValueOnce(
      new Response(
        JSON.stringify({ id: "att_1", path: "_attachments/photo1.png", description: "" }),
        { status: 200 }
      )
    );
    fetchSpy.mockResolvedValueOnce(
      new Response(
        JSON.stringify({ id: "att_2", path: "_attachments/photo2.png", description: "" }),
        { status: 200 }
      )
    );
    fetchSpy.mockResolvedValueOnce(
      new Response(
        sseStream("event: done\ndata: {\"session_id\":\"s1\"}\n\n"),
        { status: 200, headers: { "Content-Type": "text/event-stream" } }
      )
    );

    const user = userEvent.setup();
    renderHome();

    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );

    const fileInput = screen.getByTestId("file-input") as HTMLInputElement;

    // Upload files one at a time via separate picker actions.
    const file1 = new File(["data1"], "photo1.png", { type: "image/png" });
    const file2 = new File(["data2"], "photo2.png", { type: "image/png" });
    await user.upload(fileInput, file1);
    await user.upload(fileInput, file2);

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
    // Hold the upload response until we explicitly release it.
    let resolveUpload!: (r: Response) => void;
    const uploadPending = new Promise<Response>((res) => { resolveUpload = res; });

    const fetchSpy = vi.spyOn(globalThis, "fetch");
    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    fetchSpy.mockImplementationOnce(() => uploadPending);

    const user = userEvent.setup();
    renderHome();

    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );

    // Start upload — it will not complete until resolveUpload is called.
    const fileInput = screen.getByTestId("file-input") as HTMLInputElement;
    const file = new File(["data"], "photo.png", { type: "image/png" });
    await user.upload(fileInput, file);

    // Type something so the button would normally be enabled.
    await user.type(screen.getByPlaceholderText(/message autowiki/i), "hello");

    // Send button must be disabled while the upload is in flight.
    expect(screen.getByRole("button", { name: /send/i })).toBeDisabled();

    // Resolve the upload — button should become enabled again.
    resolveUpload(
      new Response(JSON.stringify({ path: "_attachments/photo.png" }), { status: 200 })
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /send/i })).not.toBeDisabled()
    );
  });

  it("does not send on Enter while an attachment is uploading", async () => {
    let resolveUpload!: (r: Response) => void;
    const uploadPending = new Promise<Response>((res) => { resolveUpload = res; });

    const fetchSpy = vi.spyOn(globalThis, "fetch");
    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    fetchSpy.mockImplementationOnce(() => uploadPending);

    const user = userEvent.setup();
    renderHome();

    await waitFor(() =>
      expect(screen.getByPlaceholderText(/message autowiki/i)).toBeInTheDocument()
    );

    const fileInput = screen.getByTestId("file-input") as HTMLInputElement;
    const file = new File(["data"], "photo.png", { type: "image/png" });
    await user.upload(fileInput, file);

    const textarea = screen.getByPlaceholderText(/message autowiki/i);
    await user.type(textarea, "hello{Enter}");

    // No /api/chat call should have been made.
    const chatCalls = fetchSpy.mock.calls.filter(([url]) => url === "/api/chat");
    expect(chatCalls).toHaveLength(0);

    // Cleanup.
    resolveUpload(
      new Response(JSON.stringify({ path: "_attachments/photo.png" }), { status: 200 })
    );
  });

  it("shows status message during streaming", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    // Stream: status event with no following delta — status should remain visible.
    fetchSpy.mockResolvedValueOnce(
      new Response(
        sseStream(
          "event: status\ndata: {\"message\":\"Reading programming/go.md\u2026\"}\n\n",
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

    await user.type(screen.getByPlaceholderText(/message autowiki/i), "tell me about Go");
    await user.click(screen.getByRole("button", { name: /send/i }));

    // Status text must appear in the assistant bubble.
    await waitFor(() =>
      expect(screen.getByText(/Reading programming\/go\.md/)).toBeInTheDocument()
    );
  });

  it("status message is replaced by the next one", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    fetchSpy.mockResolvedValueOnce(
      new Response(
        sseStream(
          "event: status\ndata: {\"message\":\"Reading first.md\u2026\"}\n\n",
          "event: status\ndata: {\"message\":\"Reading second.md\u2026\"}\n\n",
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

    await user.type(screen.getByPlaceholderText(/message autowiki/i), "tell me about Go");
    await user.click(screen.getByRole("button", { name: /send/i }));

    // Only the second status must be visible; the first must be gone.
    await waitFor(() => {
      expect(screen.getByText(/Reading second\.md/)).toBeInTheDocument();
      expect(screen.queryByText(/Reading first\.md/)).not.toBeInTheDocument();
    });
  });

  it("redirects to /login when /api/chat returns 401", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    fetchSpy.mockResolvedValueOnce(
      new Response("Unauthorized", { status: 401 })
    );

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
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    fetchSpy.mockResolvedValueOnce(
      new Response("Unauthorized", { status: 401 })
    );

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

  it("does not show vault summary when no vault event", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify(AUTH_OK), { status: 200 })
    );
    fetchSpy.mockResolvedValueOnce(
      new Response(
        sseStream(
          "event: delta\ndata: {\"text\":\"Just chatting.\"}\n\n",
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

    await user.type(screen.getByPlaceholderText(/message autowiki/i), "hello");
    await user.click(screen.getByRole("button", { name: /send/i }));

    await waitFor(() =>
      expect(screen.getByText(/Just chatting/)).toBeInTheDocument()
    );
    expect(screen.queryByText(/saved to vault/i)).not.toBeInTheDocument();
  });
});
