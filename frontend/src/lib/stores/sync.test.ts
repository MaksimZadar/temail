import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../api", () => ({
  listMailboxes: vi.fn(async () => [
    { name: "INBOX", displayName: "Inbox", messages: 100 },
    { name: "Sent", displayName: "Sent", messages: 50 },
    { name: "Labels/Work", displayName: "Work", messages: 0 },
  ]),
  syncStatus: vi.fn(async () => ({
    running: false,
    stopTimedOut: false,
    lastResult: null,
    lastError: "",
  })),
  onEvent: vi.fn(() => () => {}),
}));

import { sync, sortRows, relativeTime, type FolderRow } from "./sync.svelte";

const ev = (state: string, mailbox = "", o: Record<string, number> = {}) => ({
  state,
  mailbox,
  headersDone: 0,
  headersTotal: 0,
  bodiesDone: 0,
  bodiesTotal: 0,
  ...o,
});
const done = (errs: { mailbox: string; message: string }[] | null = null) => ({
  mailboxes: 3,
  headersFetched: 0,
  deleted: 0,
  bodiesFetched: 0,
  bodiesFailed: 0,
  mailboxErrors: errs,
});

beforeEach(async () => {
  vi.useFakeTimers();
  sync.reset();
  await sync.start();
});
afterEach(() => {
  sync.reset();
  vi.useRealTimers();
});

describe("sync store", () => {
  it("seeds every folder as waiting", () => {
    expect(sync.rows.map((r) => r.state)).toEqual([
      "waiting",
      "waiting",
      "waiting",
    ]);
    expect(sync.total).toBe(3);
  });

  it("keeps one stable line while folders interleave", () => {
    sync.onProgress(
      ev("headers", "INBOX", { headersDone: 5, headersTotal: 10 }),
    );
    sync.onProgress(ev("headers", "Sent", { headersDone: 1, headersTotal: 4 }));
    sync.onProgress(
      ev("headers", "INBOX", { headersDone: 6, headersTotal: 10 }),
    );
    sync.flush();
    const lines = new Set<string>();
    for (let i = 0; i < 6; i++) {
      sync.onProgress(
        ev("headers", i % 2 ? "INBOX" : "Sent", {
          headersDone: 1,
          headersTotal: 4,
        }),
      );
      sync.flush();
      lines.add(sync.text.replace(/\d+/, "N"));
    }
    expect(lines.size).toBe(1);
    expect(sync.text).toMatch(/^Syncing folders · \d of 3$/);
  });

  it("coalesces rapid events into one publish", () => {
    sync.onProgress(
      ev("headers", "INBOX", { headersDone: 1, headersTotal: 10 }),
    );
    sync.onProgress(
      ev("headers", "INBOX", { headersDone: 9, headersTotal: 10 }),
    );
    // Nothing published yet.
    expect(sync.rows.find((r) => r.name === "INBOX")!.headersDone).toBe(0);
    vi.advanceTimersByTime(260);
    expect(sync.rows.find((r) => r.name === "INBOX")!.headersDone).toBe(9);
  });

  it("aggregates progress and never steps backwards", () => {
    sync.onProgress(
      ev("headers", "INBOX", { headersDone: 5, headersTotal: 10 }),
    );
    sync.flush();
    const a = sync.fraction!;
    sync.onProgress(
      ev("headers", "INBOX", { headersDone: 2, headersTotal: 10 }),
    );
    sync.flush();
    expect(sync.fraction!).toBeGreaterThanOrEqual(a);
    expect(a).toBeGreaterThan(0);
    expect(a).toBeLessThan(1);
  });

  it("marks folders done in the bodies phase and counts them", () => {
    sync.onProgress(
      ev("headers", "INBOX", { headersDone: 10, headersTotal: 10 }),
    );
    sync.onProgress(ev("headers", "Sent", { headersDone: 4, headersTotal: 4 }));
    sync.onProgress(ev("bodies"));
    sync.onProgress(
      ev("bodies", "INBOX", { bodiesDone: 100, bodiesTotal: 100 }),
    );
    sync.onProgress(ev("bodies", "Sent", { bodiesDone: 10, bodiesTotal: 50 }));
    sync.flush();
    expect(sync.doneCount).toBe(1);
    expect(sync.text).toBe("Downloading messages · 1 of 3 folders");
    expect(sync.rows.find((r) => r.name === "Sent")!.state).toBe("syncing");
  });

  it("sums cached messages", () => {
    sync.onProgress(ev("bodies", "INBOX", { bodiesDone: 1, bodiesTotal: 120 }));
    sync.flush();
    expect(sync.messages).toBe(120 + 50);
  });

  it("finishes with Synced and relative time", () => {
    sync.onProgress(
      ev("headers", "INBOX", { headersDone: 1, headersTotal: 2 }),
    );
    sync.onDone(done());
    expect(sync.running).toBe(false);
    expect(sync.text).toBe("Synced · just now");
    expect(sync.rows.every((r) => r.state === "done")).toBe(true);
    expect(sync.announcement).toBe("Sync finished");
    expect(sync.fraction).toBeNull();
  });

  it("lists failed folders with the reason", () => {
    sync.onProgress(
      ev("headers", "INBOX", { headersDone: 1, headersTotal: 2 }),
    );
    sync.onDone(done([{ mailbox: "Sent", message: "timed out" }]));
    expect(sync.failed.map((r) => r.name)).toEqual(["Sent"]);
    expect(sync.failed[0].error).toBe("timed out");
    expect(sync.announcement).toMatch(/couldn't sync/);
  });

  it("reports a sync error", () => {
    sync.onProgress(ev("connecting"));
    sync.onError("Bridge went away");
    expect(sync.text).toBe("Sync failed");
    expect(sync.error).toBe("Bridge went away");
  });

  it("only changes the announcement at start and finish", () => {
    sync.onProgress(
      ev("headers", "INBOX", { headersDone: 1, headersTotal: 2 }),
    );
    const a = sync.announcement;
    sync.onProgress(ev("headers", "Sent", { headersDone: 1, headersTotal: 2 }));
    sync.onProgress(ev("bodies", "INBOX", { bodiesDone: 1, bodiesTotal: 2 }));
    expect(sync.announcement).toBe(a);
    expect(a).toBe("Sync started");
  });

  it("starts fresh on a second run", () => {
    sync.onProgress(
      ev("headers", "INBOX", { headersDone: 1, headersTotal: 2 }),
    );
    sync.onDone(done());
    sync.onProgress(ev("connecting"));
    sync.flush();
    expect(sync.running).toBe(true);
    expect(sync.rows.every((r) => r.state === "waiting")).toBe(true);
  });
});

describe("helpers", () => {
  const row = (name: string, state: FolderRow["state"]): FolderRow => ({
    name,
    display: name,
    state,
    headersDone: 0,
    headersTotal: 0,
    bodiesDone: 0,
    bodiesTotal: 0,
    messages: 0,
    error: "",
    fraction: 0,
  });
  it("sorts active, waiting, done, failed, stable inside groups", () => {
    const rows = [
      row("a", "done"),
      row("b", "failed"),
      row("c", "waiting"),
      row("d", "syncing"),
      row("e", "waiting"),
    ];
    expect(sortRows(rows).map((r) => r.name)).toEqual([
      "d",
      "c",
      "e",
      "a",
      "b",
    ]);
  });
  it("formats relative time", () => {
    expect(relativeTime(0, 10_000)).toBe("just now");
    expect(relativeTime(0, 5 * 60_000)).toBe("5 min ago");
    expect(relativeTime(0, 3 * 3600_000)).toBe("3 hours ago");
  });
});
