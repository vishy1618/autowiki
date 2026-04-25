import { describe, it, expect } from "vitest";
import { isNearBottom } from "./scrollLock";

describe("isNearBottom", () => {
  it("returns true when scrolled exactly to the bottom", () => {
    expect(isNearBottom(900, 1000, 100)).toBe(true);
  });

  it("returns true when within the default 100px threshold", () => {
    // 1000 - 850 - 100 = 50 — 50px away from bottom, within threshold
    expect(isNearBottom(850, 1000, 100)).toBe(true);
  });

  it("returns false when scrolled above the threshold", () => {
    // 1000 - 700 - 100 = 200 — 200px away from bottom, beyond threshold
    expect(isNearBottom(700, 1000, 100)).toBe(false);
  });

  it("returns false at exactly one pixel beyond the threshold", () => {
    // 1000 - 799 - 100 = 101 — just outside threshold
    expect(isNearBottom(799, 1000, 100)).toBe(false);
  });

  it("respects a custom threshold", () => {
    expect(isNearBottom(700, 1000, 100, 200)).toBe(true);
    expect(isNearBottom(700, 1000, 100, 100)).toBe(false);
  });

  it("returns true on an empty container (all zeros)", () => {
    expect(isNearBottom(0, 0, 0)).toBe(true);
  });
});
