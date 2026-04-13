import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { formatRelative } from "./formatRelative";

describe("formatRelative", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-04-13T12:00:00Z"));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("returns 'just now' for timestamps under a minute ago", () => {
    const iso = new Date(Date.now() - 30_000).toISOString();
    expect(formatRelative(iso)).toBe("just now");
  });

  it("returns minutes ago for timestamps under an hour ago", () => {
    const iso = new Date(Date.now() - 5 * 60_000).toISOString();
    expect(formatRelative(iso)).toBe("5 minutes ago");
  });

  it("returns singular minute for 1 minute ago", () => {
    const iso = new Date(Date.now() - 60_000).toISOString();
    expect(formatRelative(iso)).toBe("1 minute ago");
  });

  it("returns hours ago for timestamps under a day ago", () => {
    const iso = new Date(Date.now() - 3 * 3600_000).toISOString();
    expect(formatRelative(iso)).toBe("3 hours ago");
  });

  it("returns singular hour for 1 hour ago", () => {
    const iso = new Date(Date.now() - 3600_000).toISOString();
    expect(formatRelative(iso)).toBe("1 hour ago");
  });

  it("returns 'yesterday' for timestamps 1 day ago", () => {
    const iso = new Date(Date.now() - 24 * 3600_000).toISOString();
    expect(formatRelative(iso)).toBe("yesterday");
  });

  it("returns a date string for timestamps older than 1 day", () => {
    const iso = new Date("2026-04-10T08:00:00Z").toISOString();
    const result = formatRelative(iso);
    expect(result).toMatch(/Apr/);
    expect(result).toMatch(/10/);
  });
});
