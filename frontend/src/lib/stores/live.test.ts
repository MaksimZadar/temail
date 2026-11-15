import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const handlers: Record<string, (d: unknown) => void> = {};
vi.mock("../api", () => ({
  listMailboxes: vi.fn(),
  listMessages: vi.fn(),
  getMessage: vi.fn(),
  syncStatus: vi.fn(),
  syncStart: vi.fn(async () => {}),
  onEvent: vi.fn((name: string, fn: (d: unknown) => void) => {
    handlers[name] = fn;
    return () => delete handlers[name];
  }),
}));

import * as api from "../api";
import { sync } from "./sync.svelte";
import { Mail } from "./mail.svelte";
import { keyOf, mergeHead } from "../mail/format";
import { hdr } from "../../test-helpers";
import { ui } from "./ui.svelte";

const m = vi.mocked(api);
const status = (o: Partial<api.SyncStatus> = {}): api.SyncStatus => ({
  running: false,
  watching: false,
  idle: false,
  lastChecked: null,
  stopTimedOut: false,
  lastResult: null,
  lastError: "",
  ...o,
});
const box = (
  name: string,
  kind: api.MailboxKind,
  unseen = 0,
): api.MailboxDTO => ({
  name,
  displayName: name,
  kind,
  uidValidity: 7,
  messages: 10,
  bodies: 0,
  unseen,
  lastSync: null,
});
const ev = (state: string, mailbox = "") => ({
  state,
  mailbox,
  headersDone: 1,
  headersTotal: 2,
  bodiesDone: 0,
  bodiesTotal: 0,
});

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-01-01T12:00:00Z"));
  vi.clearAllMocks();
  m.listMailboxes.mockResolvedValue([box("INBOX", "inbox", 2)]);
  sync.reset();
});
afterEach(() => {
  sync.reset();
  vi.useRealTimers();
});

describe("footer states and the meaning of running", () => {
  it("running is the initial sync only: the footer shows Live once watching", async () => {
    m.syncStatus.mockResolvedValue(
      status({ running: false, watching: true, idle: true }),
    );
    await sync.start();
    expect(sync.running).toBe(false);
    expect(sync.text).toBe("Live");
    expect(sync.fraction).toBeNull();
  });

  it("shows Syncing… while the initial sync runs", async () => {
    m.syncStatus.mockResolvedValue(status({ running: true }));
    await sync.start();
    expect(sync.running).toBe(true);
    expect(sync.text).toBe("Syncing…");
  });

  it("shows a calm 'Up to date · checked …' with a polling hint when IDLE is down", async () => {
    const t = new Date(Date.now() - 2 * 60_000).toISOString();
    m.syncStatus.mockResolvedValue(
      status({ watching: true, idle: false, lastChecked: t }),
    );
    await sync.start();
    expect(sync.text).toBe("Up to date · checked 2 min ago");
    expect(sync.polling).toBe(true);
    expect(sync.detail).toMatch(/live connection is down/);
  });

  it("Live has no polling hint", async () => {
    m.syncStatus.mockResolvedValue(status({ watching: true, idle: true }));
    await sync.start();
    expect(sync.polling).toBe(false);
    expect(sync.detail).toMatch(/Watching for new mail/);
  });

  it("the spinner stops when the initial sync is done and the watcher is up", async () => {
    m.syncStatus.mockResolvedValue(status({ running: true }));
    await sync.start();
    handlers["sync:progress"](ev("headers", "INBOX"));
    expect(sync.running).toBe(true);
    m.syncStatus.mockResolvedValue(status({ watching: true, idle: true }));
    handlers["sync:done"]({
      mailboxes: 1,
      headersFetched: 1,
      deleted: 0,
      bodiesFetched: 0,
      bodiesFailed: 0,
      mailboxErrors: null,
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(sync.running).toBe(false);
    expect(sync.text).toBe("Live");
  });

  it("progress events while watching don't flip the footer back to Syncing", async () => {
    m.syncStatus.mockResolvedValue(status({ watching: true, idle: true }));
    await sync.start();
    handlers["sync:progress"](ev("headers", "INBOX"));
    handlers["sync:progress"](ev("bodies", "INBOX"));
    expect(sync.running).toBe(false);
    expect(sync.text).toBe("Live");
  });

  it("sync:changed refreshes lastChecked", async () => {
    m.syncStatus.mockResolvedValue(status({ watching: true, idle: false }));
    await sync.start();
    expect(sync.text).toBe("Up to date");
    m.syncStatus.mockResolvedValue(
      status({
        watching: true,
        idle: false,
        lastChecked: new Date().toISOString(),
      }),
    );
    handlers["sync:changed"]({ mailboxes: ["INBOX"] });
    await vi.advanceTimersByTimeAsync(0);
    expect(sync.text).toBe("Up to date · checked just now");
  });

  it("a watch error ends the live state", async () => {
    m.syncStatus.mockResolvedValue(status({ watching: true, idle: true }));
    await sync.start();
    handlers["sync:error"]({ message: "Bridge rejected the login" });
    expect(sync.watching).toBe(false);
    expect(sync.text).toBe("Sync failed");
  });

  it("the relative time moves on by itself", async () => {
    const t = new Date(Date.now()).toISOString();
    m.syncStatus.mockResolvedValue(
      status({ watching: true, idle: false, lastChecked: t }),
    );
    await sync.start();
    expect(sync.text).toBe("Up to date · checked just now");
    vi.setSystemTime(Date.now() + 5 * 60_000);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(sync.text).toMatch(/checked \d+ min ago/);
  });
});

describe("honest wording and Sync now", () => {
  it("the Live tooltip says what is instant and what can lag", async () => {
    m.syncStatus.mockResolvedValue(status({ watching: true, idle: true }));
    await sync.start();
    expect(sync.text).toBe("Live");
    expect(sync.detail).toMatch(/New mail appears instantly/);
    expect(sync.detail).toMatch(/read and star changes made elsewhere may take a few minutes/);
  });

  it("never says Live unless the live connection is up", async () => {
    m.syncStatus.mockResolvedValue(status({ watching: true, idle: false }));
    await sync.start();
    expect(sync.text).not.toBe("Live");
    expect(sync.text).toMatch(/^Up to date/);
  });

  it("Sync now while watching just asks for a check", async () => {
    m.syncStatus.mockResolvedValue(status({ watching: true, idle: true }));
    await sync.start();
    await sync.syncNow();
    expect(m.syncStart).toHaveBeenCalledTimes(1);
    expect(ui.hint).toBe("Checking for new mail…");
    m.syncStatus.mockResolvedValue(
      status({ watching: true, idle: true, lastChecked: new Date().toISOString() }),
    );
    await vi.advanceTimersByTimeAsync(1600);
    expect(sync.lastChecked).not.toBeNull();
  });

  it("Sync now doesn't start another run during the first sync", async () => {
    m.syncStatus.mockResolvedValue(status({ running: true }));
    await sync.start();
    await sync.syncNow();
    expect(m.syncStart).not.toHaveBeenCalled();
    expect(ui.hint).toBe("Already syncing");
  });

  it("reports a failed Sync now", async () => {
    m.syncStatus.mockResolvedValue(status({ watching: true, idle: true }));
    await sync.start();
    m.syncStart.mockRejectedValueOnce(new Error("login refused"));
    await sync.syncNow();
    expect(ui.hint).toBe("Couldn't start a sync");
  });

  it("a progress event from a watcher check doesn't leave a stuck spinner", async () => {
    // Status not yet known to say "watching": the event looks like a run start.
    m.syncStatus.mockResolvedValue(status({ running: false, watching: false }));
    await sync.start();
    handlers["sync:progress"](ev("headers", "INBOX"));
    expect(sync.running).toBe(true); // optimistic
    await vi.advanceTimersByTimeAsync(1100);
    expect(sync.running).toBe(false); // the status says it is not the first sync
  });

  it("a stray sync:done is not treated as the end of the first sync", async () => {
    m.syncStatus.mockResolvedValue(status({ running: true }));
    await sync.start();
    handlers["sync:done"]({
      mailboxes: 1, headersFetched: 0, deleted: 0, bodiesFetched: 0, bodiesFailed: 0, mailboxErrors: null,
    });
    m.syncStatus.mockResolvedValue(status({ running: true }));
    await vi.advanceTimersByTimeAsync(0);
    expect(sync.running).toBe(true);
  });
});

describe("mergeHead removal", () => {
  const list = [5, 4, 3, 2, 1].map((u) => hdr(u));
  it("drops a loaded message that is missing from a full fresh page", () => {
    const out = mergeHead(
      list,
      [5, 4, 2].map((u) => hdr(u)),
      false,
    );
    // 3 is newer than the oldest fresh item (2) and missing: removed. 1 is older: kept.
    expect(out.map((h) => h.uid)).toEqual([5, 4, 2, 1]);
  });
  it("when the fresh page is the whole folder, everything missing goes", () => {
    const out = mergeHead(
      list,
      [5, 2].map((u) => hdr(u)),
      true,
    );
    expect(out.map((h) => h.uid)).toEqual([5, 2]);
  });
  it("an empty complete page empties the list", () => {
    expect(mergeHead(list, [], true)).toEqual([]);
  });
  it("keeps the same array when nothing changed", () => {
    expect(mergeHead(list, list.slice(0, 3), false)).toBe(list);
  });
  it("notices a label change", () => {
    const withLabel = [
      hdr(5, {
        mailboxes: [{ name: "Labels/A", displayName: "A", kind: "label" }],
      }),
    ];
    const out = mergeHead([hdr(5, { mailboxes: [] })], withLabel, true);
    expect(out[0].mailboxes?.[0].name).toBe("Labels/A");
  });
});

describe("live changes in the open list", () => {
  const page = (uids: number[], next = true) => ({
    messages: uids.map((u) => hdr(u)),
    next: next ? { date: "2026-01-01T00:00:00Z", uid: 1 } : null,
  });
  let mail: Mail;
  async function open() {
    m.listMailboxes.mockResolvedValue([
      box("INBOX", "inbox", 2),
      box("Sent", "sent"),
    ]);
    m.listMessages.mockResolvedValue(page([5, 4, 3, 2, 1], false));
    mail = new Mail();
    await mail.loadFolders();
    m.listMailboxes.mockClear();
    m.listMessages.mockClear();
  }

  it("coalesces a burst into one refresh and keeps the cursor on its message", async () => {
    await open();
    mail.cursorTo(2); // uid 3
    const key = keyOf(mail.shown[mail.cursorIndex]);
    m.listMessages.mockResolvedValue(page([8, 7, 6, 5, 4]));
    mail.noteChanged(["INBOX"]);
    mail.noteChanged(["INBOX", "Labels/X"]);
    mail.noteChanged(["INBOX"]);
    await vi.advanceTimersByTimeAsync(100);
    expect(m.listMessages).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(200);
    expect(m.listMailboxes).toHaveBeenCalledTimes(1);
    expect(m.listMessages).toHaveBeenCalledTimes(1);
    expect(keyOf(mail.shown[mail.cursorIndex])).toBe(key);
    expect(mail.messages.map((x) => x.uid)).toEqual([8, 7, 6, 5, 4, 3, 2, 1]);
  });

  it("updates only the counts when another mailbox changed", async () => {
    await open();
    m.listMailboxes.mockResolvedValue([
      box("INBOX", "inbox", 2),
      box("Sent", "sent", 4),
    ]);
    mail.noteChanged(["Sent"]);
    await vi.advanceTimersByTimeAsync(300);
    expect(m.listMailboxes).toHaveBeenCalledTimes(1);
    expect(m.listMessages).not.toHaveBeenCalled();
    expect(mail.folders.find((f) => f.name === "Sent")?.unseen).toBe(4);
  });

  it("an empty change list means everything", async () => {
    await open();
    mail.noteChanged([]);
    await vi.advanceTimersByTimeAsync(300);
    expect(m.listMessages).toHaveBeenCalledTimes(1);
  });

  it("removes messages that disappeared and moves the cursor to its neighbour", async () => {
    await open();
    mail.cursorTo(2); // uid 3
    m.listMessages.mockResolvedValue(page([5, 4, 2, 1], false));
    mail.noteChanged(["INBOX"]);
    await vi.advanceTimersByTimeAsync(300);
    expect(mail.messages.map((x) => x.uid)).toEqual([5, 4, 2, 1]);
    // The removed message's place is taken by the next one down.
    expect(mail.shown[mail.cursorIndex].uid).toBe(2);
  });

  it("keeps showing an open message that was removed, with a notice flag", async () => {
    await open();
    m.getMessage.mockResolvedValue({
      ...hdr(3),
      textBody: "t",
      htmlBody: "",
      attachments: null,
    });
    await mail.open(mail.messages[2]);
    m.listMessages.mockResolvedValue(page([5, 4, 2, 1], false));
    mail.noteChanged(["INBOX"]);
    await vi.advanceTimersByTimeAsync(300);
    expect(mail.openGone).toBe(true);
    expect(mail.message?.uid).toBe(3);
  });

  it("re-reads the open message when its labels may have changed", async () => {
    await open();
    const before = {
      ...hdr(3),
      textBody: "t",
      htmlBody: "",
      attachments: null,
      mailboxes: [],
    };
    m.getMessage.mockResolvedValue(before);
    await mail.open(mail.messages[2]);
    m.getMessage.mockClear();
    m.getMessage.mockResolvedValue({
      ...before,
      mailboxes: [{ name: "Labels/A", displayName: "A", kind: "label" }],
    });
    m.listMessages.mockResolvedValue(page([5, 4, 3, 2, 1]));
    mail.noteChanged(["INBOX"]);
    await vi.advanceTimersByTimeAsync(300);
    expect(mail.message?.mailboxes?.[0].displayName).toBe("A");
  });

  it("a refresh queued behind a page in flight still runs", async () => {
    await open();
    let release!: (p: ReturnType<typeof page>) => void;
    m.listMessages.mockReturnValueOnce(new Promise((r) => (release = r)));
    mail.next = { date: "2026-01-01T00:00:00Z", uid: 1 };
    const more = mail.loadPage(false);
    mail.noteChanged(["INBOX"]);
    await vi.advanceTimersByTimeAsync(300);
    m.listMessages.mockResolvedValue(page([9, 8, 7, 6, 5]));
    release(page([2, 1], false));
    await more;
    await vi.advanceTimersByTimeAsync(0);
    expect(m.listMessages.mock.calls.length).toBeGreaterThanOrEqual(2);
  });
});
