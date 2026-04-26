import { renderHook, waitFor, act } from "@testing-library/react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { useChatHistory } from "./useChatHistory";

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
    return new Response(JSON.stringify({ sessions: [] }), { status: 200 });
  });
}

function sessionsResp(sessions: Array<{ id: string; created_at: string; last_active_at: string }>) {
  return new Response(JSON.stringify({ sessions }), { status: 200 });
}

function messagesResp(messages: Array<{ id: string; role: string; content: string; created_at: string }>) {
  return new Response(JSON.stringify({ messages }), { status: 200 });
}

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("useChatHistory", () => {
  it("loads sessions on mount when ready", async () => {
    const fetchSpy = mockFetch();
    renderHook(() => useChatHistory({ ready: true }));

    await waitFor(() => {
      expect(fetchSpy.mock.calls.some(([url]) =>
        String(url).includes("/api/chat-sessions?limit=")
      )).toBe(true);
    });
  });

  it("does not load sessions when not ready", async () => {
    const fetchSpy = mockFetch();
    renderHook(() => useChatHistory({ ready: false }));

    await new Promise((r) => setTimeout(r, 50));
    expect(fetchSpy.mock.calls.filter(([url]) =>
      String(url).includes("/api/chat-sessions")
    )).toHaveLength(0);
  });

  it("returns messages loaded from sessions", async () => {
    mockFetch({
      "/api/chat-sessions": [
        sessionsResp([{ id: "s1", created_at: "2026-04-14T09:00:00Z", last_active_at: "2026-04-14T09:05:00Z" }]),
      ],
      "/api/chat-sessions/s1": [
        messagesResp([{ id: "m1", role: "user", content: "hello history", created_at: "2026-04-14T09:00:00Z" }]),
      ],
    });

    const { result } = renderHook(() => useChatHistory({ ready: true }));

    await waitFor(() => {
      const msgs = result.current.messages.filter((m) => "role" in m);
      expect(msgs).toHaveLength(1);
      expect((msgs[0] as { content: string }).content).toBe("hello history");
    });
  });

  it("inserts a session divider between multiple sessions", async () => {
    mockFetch({
      "/api/chat-sessions": [
        sessionsResp([
          { id: "s2", created_at: "2026-04-14T10:00:00Z", last_active_at: "2026-04-14T10:05:00Z" },
          { id: "s1", created_at: "2026-04-14T09:00:00Z", last_active_at: "2026-04-14T09:05:00Z" },
        ]),
      ],
      "/api/chat-sessions/s2": [messagesResp([{ id: "m2", role: "user", content: "b", created_at: "2026-04-14T10:00:00Z" }])],
      "/api/chat-sessions/s1": [messagesResp([{ id: "m1", role: "user", content: "a", created_at: "2026-04-14T09:00:00Z" }])],
    });

    const { result } = renderHook(() => useChatHistory({ ready: true }));

    await waitFor(() => {
      const dividers = result.current.messages.filter((m) => "kind" in m && m.kind === "divider");
      expect(dividers).toHaveLength(1);
    });
  });

  it("sets hasMoreHistory true when full page returned, false when partial", async () => {
    // Returns exactly HISTORY_LIMIT (3) sessions → more expected
    mockFetch({
      "/api/chat-sessions": [
        sessionsResp([
          { id: "s3", created_at: "2026-04-14T11:00:00Z", last_active_at: "2026-04-14T11:05:00Z" },
          { id: "s2", created_at: "2026-04-14T10:00:00Z", last_active_at: "2026-04-14T10:05:00Z" },
          { id: "s1", created_at: "2026-04-14T09:00:00Z", last_active_at: "2026-04-14T09:05:00Z" },
        ]),
      ],
      "/api/chat-sessions/s3": [messagesResp([])],
      "/api/chat-sessions/s2": [messagesResp([])],
      "/api/chat-sessions/s1": [messagesResp([])],
    });

    const { result } = renderHook(() => useChatHistory({ ready: true }));

    await waitFor(() => expect(result.current.historyLoading).toBe(false));
    expect(result.current.hasMoreHistory).toBe(true);
  });

  it("prepends older messages when loadHistory is called with prepend=true", async () => {
    mockFetch({
      "/api/chat-sessions": [
        // Initial load
        sessionsResp([{ id: "s2", created_at: "2026-04-14T10:00:00Z", last_active_at: "2026-04-14T10:05:00Z" }]),
        // Older page
        sessionsResp([{ id: "s1", created_at: "2026-04-14T09:00:00Z", last_active_at: "2026-04-14T09:05:00Z" }]),
      ],
      "/api/chat-sessions/s2": [messagesResp([{ id: "m2", role: "user", content: "newer", created_at: "2026-04-14T10:00:00Z" }])],
      "/api/chat-sessions/s1": [messagesResp([{ id: "m1", role: "user", content: "older", created_at: "2026-04-14T09:00:00Z" }])],
    });

    const { result } = renderHook(() => useChatHistory({ ready: true }));

    await waitFor(() => expect(result.current.historyLoading).toBe(false));

    await act(async () => {
      await result.current.loadHistory(1, true);
    });

    const contents = result.current.messages
      .filter((m) => "role" in m)
      .map((m) => (m as { content: string }).content);

    expect(contents[0]).toBe("older");
    expect(contents[contents.length - 1]).toBe("newer");
  });

  it("records latestActivityRef from the newest session after load", async () => {
    mockFetch({
      "/api/chat-sessions": [
        sessionsResp([{ id: "s1", created_at: "2026-04-14T09:00:00Z", last_active_at: "2026-04-14T09:05:00Z" }]),
      ],
      "/api/chat-sessions/s1": [messagesResp([])],
    });

    const { result } = renderHook(() => useChatHistory({ ready: true }));

    await waitFor(() => expect(result.current.historyLoading).toBe(false));
    expect(result.current.latestActivityRef.current).toBe("2026-04-14T09:05:00Z");
  });
});
