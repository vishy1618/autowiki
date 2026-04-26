import { renderHook, waitFor, act } from "@testing-library/react";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { useChatStream } from "./useChatStream";
import type { ChatItem } from "./useChatHistory";

type FetchEntry = Response | Promise<Response>;

function mockFetch(overrides: Record<string, FetchEntry | FetchEntry[]> = {}) {
  const queues = new Map<string, FetchEntry[]>();
  for (const [url, val] of Object.entries(overrides)) {
    queues.set(url, Array.isArray(val) ? [...val] : [val]);
  }
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = input instanceof Request ? input.url : String(input);
    const sortedEntries = [...queues.entries()].sort((a, b) => b[0].length - a[0].length);
    for (const [pattern, queue] of sortedEntries) {
      const matches = url === pattern || url.startsWith(pattern + "/") || url.startsWith(pattern + "?");
      if (matches && queue.length > 0) return queue.shift()!;
    }
    return new Response("not found", { status: 404 });
  });
}

function sseStream(...chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  return new ReadableStream({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(encoder.encode(chunk));
      controller.close();
    },
  });
}

function chatSSE(...chunks: string[]) {
  return new Response(sseStream(...chunks), {
    status: 200,
    headers: { "Content-Type": "text/event-stream" },
  });
}

const SSE_DONE = "event: done\ndata: {\"session_id\":\"s1\"}\n\n";
const SSE_ERROR = (msg = "network error") =>
  `event: error\ndata: ${JSON.stringify({ message: msg })}\n\n`;

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("useChatStream", () => {
  it("sets sending=true while streaming and false after done", async () => {
    mockFetch({ "/api/chat": [chatSSE(SSE_DONE)] });
    const setMessages = vi.fn();
    const { result } = renderHook(() => useChatStream({ setMessages, navigate: vi.fn() }));

    expect(result.current.sending).toBe(false);

    act(() => { result.current.doStream("hello", [], 0); });

    await waitFor(() => expect(result.current.sending).toBe(false));
  });

  it("appends assistant bubble before streaming starts", async () => {
    mockFetch({ "/api/chat": [chatSSE(SSE_DONE)] });
    const setMessages = vi.fn();
    const { result } = renderHook(() => useChatStream({ setMessages, navigate: vi.fn() }));

    act(() => { result.current.doStream("hello", [], 0); });

    await waitFor(() => expect(setMessages).toHaveBeenCalled());
    const firstCall = setMessages.mock.calls[0][0];
    const appended = firstCall([]);
    expect(appended).toHaveLength(1);
    expect(appended[0]).toMatchObject({ role: "assistant", content: "", streaming: true });
  });

  it("accumulates delta text into the assistant bubble", async () => {
    mockFetch({
      "/api/chat": [chatSSE(
        "event: delta\ndata: {\"text\":\"Hello\"}\n\n",
        "event: delta\ndata: {\"text\":\" world\"}\n\n",
        SSE_DONE
      )],
    });
    const messages: ChatItem[] = [{ role: "assistant", content: "", streaming: true }];
    const setMessages = vi.fn((updater: (prev: ChatItem[]) => ChatItem[]) => {
      messages.splice(0, messages.length, ...updater([...messages]));
    });
    const { result } = renderHook(() => useChatStream({ setMessages, navigate: vi.fn() }));

    await act(async () => { await result.current.doStream("hi", [], 0); });

    const last = messages[messages.length - 1] as { content: string };
    expect(last.content).toBe("Hello world");
  });

  it("puts server error body into the assistant bubble for non-retriable errors", async () => {
    mockFetch({ "/api/chat": [new Response("bad request", { status: 400 })] });
    const messages: ChatItem[] = [{ role: "assistant", content: "", streaming: true }];
    const setMessages = vi.fn((updater: (prev: ChatItem[]) => ChatItem[]) => {
      messages.splice(0, messages.length, ...updater([...messages]));
    });
    const { result } = renderHook(() => useChatStream({ setMessages, navigate: vi.fn() }));

    await act(async () => { await result.current.doStream("oops", [], 0); });

    const last = messages[messages.length - 1] as { content: string; streaming?: boolean };
    expect(last.content).toBe("bad request");
    expect(last.streaming).toBe(false);
  });

  it("sets retrying error message on first 5xx and retries", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchSpy = mockFetch({
      "/api/chat": [
        new Response("server error", { status: 500 }),
        chatSSE(SSE_DONE),
      ],
    });
    const setMessages = vi.fn();
    const { result } = renderHook(() => useChatStream({ setMessages, navigate: vi.fn() }));

    act(() => { result.current.doStream("hello", [], 0); });
    await waitFor(() => expect(result.current.error).toMatch(/retrying/i));

    await act(async () => { vi.advanceTimersByTime(4000); });

    await waitFor(() => {
      const chatCalls = fetchSpy.mock.calls.filter(([url]) => url === "/api/chat");
      expect(chatCalls).toHaveLength(2);
    });
    vi.useRealTimers();
  });

  it("sets error from SSE error event on final attempt", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    mockFetch({
      "/api/chat": [
        chatSSE(SSE_ERROR("fail 1")),
        chatSSE(SSE_ERROR("fail 2")),
        chatSSE(SSE_ERROR("fail 3")),
      ],
    });
    const setMessages = vi.fn();
    const { result } = renderHook(() => useChatStream({ setMessages, navigate: vi.fn() }));

    act(() => { result.current.doStream("hello", [], 0); });
    await waitFor(() => expect(result.current.error).toMatch(/retrying/i));
    await act(async () => { vi.advanceTimersByTime(4000); });
    await waitFor(() => expect(result.current.error).toMatch(/retrying/i));
    await act(async () => { vi.advanceTimersByTime(4000); });

    await waitFor(() => expect(result.current.error).toBe("fail 3"));
    vi.useRealTimers();
  });

  it("calls navigate to /login on 401 response", async () => {
    mockFetch({ "/api/chat": [new Response("Unauthorized", { status: 401 })] });
    const navigate = vi.fn();
    const { result } = renderHook(() => useChatStream({ setMessages: vi.fn(), navigate }));

    await act(async () => { await result.current.doStream("hello", [], 0); });

    expect(navigate).toHaveBeenCalledWith("/login", { replace: true });
  });

  it("attaches vault changes to the assistant bubble on vault SSE event", async () => {
    mockFetch({
      "/api/chat": [chatSSE(
        "event: vault\ndata: {\"changes\":[{\"path\":\"notes/test.md\"}]}\n\n",
        SSE_DONE
      )],
    });
    const messages: ChatItem[] = [{ role: "assistant", content: "", streaming: true }];
    const setMessages = vi.fn((updater: (prev: ChatItem[]) => ChatItem[]) => {
      messages.splice(0, messages.length, ...updater([...messages]));
    });
    const { result } = renderHook(() => useChatStream({ setMessages, navigate: vi.fn() }));

    await act(async () => { await result.current.doStream("save this", [], 0); });

    const last = messages[messages.length - 1] as { vaultChanges?: Array<{ path: string }> };
    expect(last.vaultChanges).toEqual([{ path: "notes/test.md" }]);
  });
});
