import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const handlers: Record<string, () => void> = {};
vi.mock("../api", () => ({
  cacheInfo: vi.fn(),
  onEvent: vi.fn((name: string, fn: () => void) => {
    handlers[name] = fn;
    return () => delete handlers[name];
  }),
}));

import * as api from "../api";
import { cache, THROTTLE_MS } from "./cache.svelte";
import { formatBytes } from "../mail/format";

const m = vi.mocked(api);
const info = (bytes: number) => ({ bytes, messages: 10, bodies: 8 });

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
  vi.clearAllMocks();
  cache.reset();
  m.cacheInfo.mockResolvedValue(info(1000));
});
afterEach(() => {
  cache.reset();
  vi.useRealTimers();
});

describe("formatBytes", () => {
  it.each([
    [0, "0 B"],
    [1, "1 B"],
    [1023, "1023 B"],
    [1024, "1.0 KB"],
    [1536, "1.5 KB"],
    [10 * 1024, "10 KB"],
    [1024 * 1024 - 1, "1.0 MB"],
    [5 * 1024 * 1024, "5.0 MB"],
    [Math.round(1.2 * 1024 ** 3), "1.2 GB"],
    [250 * 1024 ** 2, "250 MB"],
    [3 * 1024 ** 4, "3.0 TB"],
    [-5, "0 B"],
    [NaN, "0 B"],
  ])("%d -> %s", (n, text) => {
    expect(formatBytes(n)).toBe(text);
  });
});

describe("cache store", () => {
  it("loads on start", async () => {
    cache.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(cache.info?.bytes).toBe(1000);
    expect(m.cacheInfo).toHaveBeenCalledTimes(1);
  });

  it("throttles reloads during sync progress to one per 5 s", async () => {
    cache.start();
    await vi.advanceTimersByTimeAsync(0);
    for (let i = 0; i < 50; i++) handlers["sync:progress"]();
    await vi.advanceTimersByTimeAsync(THROTTLE_MS - 100);
    expect(m.cacheInfo).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(200);
    expect(m.cacheInfo).toHaveBeenCalledTimes(2);
    handlers["sync:progress"]();
    await vi.advanceTimersByTimeAsync(THROTTLE_MS - 400);
    expect(m.cacheInfo).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(500);
    expect(m.cacheInfo).toHaveBeenCalledTimes(3);
  });

  it("reloads right away when a sync finishes", async () => {
    cache.start();
    await vi.advanceTimersByTimeAsync(0);
    m.cacheInfo.mockResolvedValue(info(5000));
    handlers["sync:done"]();
    await vi.advanceTimersByTimeAsync(0);
    expect(cache.info?.bytes).toBe(5000);
  });

  it("hides the item on error and recovers later", async () => {
    cache.start();
    await vi.advanceTimersByTimeAsync(0);
    m.cacheInfo.mockRejectedValueOnce(new Error("boom"));
    handlers["sync:done"]();
    await vi.advanceTimersByTimeAsync(0);
    expect(cache.info).toBeNull();
    handlers["sync:done"]();
    await vi.advanceTimersByTimeAsync(0);
    expect(cache.info?.bytes).toBe(1000);
  });

  it("stops listening on stop", async () => {
    cache.start();
    cache.stop();
    expect(handlers["sync:progress"]).toBeUndefined();
  });
});
