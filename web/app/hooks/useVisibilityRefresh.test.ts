import { renderHook, waitFor, act } from "@testing-library/react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { useVisibilityRefresh } from "./useVisibilityRefresh";

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

function sessionsResp(sessions: Array<{ id: string; last_active_at: string }>) {
  return new Response(JSON.stringify({ sessions }), { status: 200 });
}

function goVisible() {
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  act(() => document.dispatchEvent(new Event("visibilitychange")));
}

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("useVisibilityRefresh", () => {
  it("fetches latest session metadata when the tab becomes visible", async () => {
    const fetchSpy = mockFetch({
      "/api/chat-sessions?limit=1": [sessionsResp([])],
    });
    const latestActivityRef = { current: null as string | null };
    const onRefresh = vi.fn();

    renderHook(() => useVisibilityRefresh({ ready: true, latestActivityRef, onRefresh }));

    const callsBefore = fetchSpy.mock.calls.filter(
      ([url]) => String(url) === "/api/chat-sessions?limit=1"
    ).length;

    goVisible();

    await waitFor(() => {
      const callsAfter = fetchSpy.mock.calls.filter(
        ([url]) => String(url) === "/api/chat-sessions?limit=1"
      ).length;
      expect(callsAfter).toBeGreaterThan(callsBefore);
    });
  });

  it("calls onRefresh when server last_active_at is newer than ref", async () => {
    mockFetch({
      "/api/chat-sessions?limit=1": [
        sessionsResp([{ id: "s1", last_active_at: "2026-04-14T09:10:00Z" }]),
      ],
    });
    const latestActivityRef = { current: "2026-04-14T09:05:00Z" as string | null };
    const onRefresh = vi.fn();

    renderHook(() => useVisibilityRefresh({ ready: true, latestActivityRef, onRefresh }));
    goVisible();

    await waitFor(() => expect(onRefresh).toHaveBeenCalledOnce());
  });

  it("does not call onRefresh when server last_active_at is unchanged", async () => {
    mockFetch({
      "/api/chat-sessions?limit=1": [
        sessionsResp([{ id: "s1", last_active_at: "2026-04-14T09:05:00Z" }]),
      ],
    });
    const latestActivityRef = { current: "2026-04-14T09:05:00Z" as string | null };
    const onRefresh = vi.fn();

    renderHook(() => useVisibilityRefresh({ ready: true, latestActivityRef, onRefresh }));
    goVisible();

    await waitFor(() =>
      expect(
        vi.mocked(fetch).mock.calls.some(([url]) => String(url) === "/api/chat-sessions?limit=1")
      ).toBe(true)
    );
    await new Promise((r) => setTimeout(r, 50));
    expect(onRefresh).not.toHaveBeenCalled();
  });

  it("calls onRefresh when latestActivityRef is null and sessions exist", async () => {
    mockFetch({
      "/api/chat-sessions?limit=1": [
        sessionsResp([{ id: "s1", last_active_at: "2026-04-14T09:05:00Z" }]),
      ],
    });
    const latestActivityRef = { current: null as string | null };
    const onRefresh = vi.fn();

    renderHook(() => useVisibilityRefresh({ ready: true, latestActivityRef, onRefresh }));
    goVisible();

    await waitFor(() => expect(onRefresh).toHaveBeenCalledOnce());
  });

  it("does not call onRefresh when sessions list is empty", async () => {
    mockFetch({
      "/api/chat-sessions?limit=1": [sessionsResp([])],
    });
    const latestActivityRef = { current: null as string | null };
    const onRefresh = vi.fn();

    renderHook(() => useVisibilityRefresh({ ready: true, latestActivityRef, onRefresh }));
    goVisible();

    await waitFor(() =>
      expect(
        vi.mocked(fetch).mock.calls.some(([url]) => String(url) === "/api/chat-sessions?limit=1")
      ).toBe(true)
    );
    await new Promise((r) => setTimeout(r, 50));
    expect(onRefresh).not.toHaveBeenCalled();
  });

  it("does not fetch when ready is false", async () => {
    const fetchSpy = mockFetch();
    const latestActivityRef = { current: null as string | null };
    const onRefresh = vi.fn();

    renderHook(() => useVisibilityRefresh({ ready: false, latestActivityRef, onRefresh }));
    goVisible();

    await new Promise((r) => setTimeout(r, 50));
    expect(fetchSpy.mock.calls.filter(([url]) => String(url) === "/api/chat-sessions?limit=1")).toHaveLength(0);
  });
});
