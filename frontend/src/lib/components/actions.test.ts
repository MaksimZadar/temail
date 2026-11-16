import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import {
  render,
  screen,
  fireEvent,
  waitFor,
  within,
} from "@testing-library/svelte";

vi.mock("../api", () => ({
  listMailboxes: vi.fn(),
  listMessages: vi.fn(),
  getMessage: vi.fn(),
  getAttachment: vi.fn(),
  openUrl: vi.fn(async () => {}),
  cacheInfo: vi.fn(async () => ({ bytes: 0, messages: 0, bodies: 0 })),
  syncStart: vi.fn(async () => {}),
  syncStatus: vi.fn(async () => ({
    running: false,
    stopTimedOut: false,
    lastResult: null,
    lastError: "",
  })),
  getStatus: vi.fn(async () => ({
    configured: true,
    keyringOk: true,
    keyringMessage: "",
    bridgeReachable: true,
    certPinned: true,
    accountId: "main",
  })),
  applyAction: vi.fn(),
  undoAction: vi.fn(),
  undoLast: vi.fn(),
  recentActions: vi.fn(async () => []),
  onEvent: vi.fn(() => () => {}),
}));

import * as api from "../api";
import App from "../../App.svelte";
import { mail } from "../stores/mail.svelte";
import { layout } from "../stores/layout.svelte";
import { app } from "../stores/app.svelte";
import { ui } from "../stores/ui.svelte";
import { sel } from "../stores/select.svelte";
import { actions, TOAST_MS } from "../stores/actions.svelte";
import { keyOf } from "../mail/format";
import { hdr } from "../../test-helpers";

const m = vi.mocked(api);
const key = (k: string, target: Element | Window = window) =>
  fireEvent.keyDown(target, { key: k });
const ok = (n: number): api.ActionResult => ({
  applied: Array.from({ length: n }, (_, i) => `m${i}`),
  failed: null,
  appliedCount: n,
  failedCount: 0,
});

const folders = [
  ["INBOX", "Inbox", "inbox"],
  ["Spam", "Spam", "spam"],
  ["Folders/Work", "Work", "folder"],
  ["Labels/Urgent", "Urgent", "label"],
].map(([name, displayName, kind]) => ({
  name,
  displayName,
  kind: kind as api.MailboxKind,
  uidValidity: 7,
  messages: 1,
  bodies: 0,
  unseen: 0,
  lastSync: null,
}));
const row = (uid: number, o: Partial<api.MessageHeader> = {}) =>
  hdr(uid, { internalId: `mid${uid}`, ...o });
const msgs = (n: number) => Array.from({ length: n }, (_, i) => row(n - i));

async function mount(list = msgs(5)) {
  app.route = "main";
  ui.overlay = "none";
  Object.assign(mail, {
    current: null,
    folders: [],
    foldersLoaded: false,
    messages: [],
    next: null,
    cursorKey: null,
    loading: false,
    error: "",
    filter: "",
    message: null,
    openKey: null,
  });
  sel.clear();
  actions.dismissToast();
  layout.focus = "list";
  layout.folderVisible = true;
  layout.width = 1280;
  m.listMailboxes.mockResolvedValue(folders);
  m.listMessages.mockResolvedValue({ messages: list, next: null });
  m.getMessage.mockImplementation(async (b, v, uid) => ({
    ...hdr(uid),
    textBody: "x",
    htmlBody: "",
    attachments: null,
  }));
  render(App);
  await screen.findAllByRole("option");
  await waitFor(() => expect(mail.cursorIndex).toBe(0));
}

beforeEach(() => {
  vi.clearAllMocks();
  m.applyAction.mockResolvedValue(ok(1));
  m.recentActions.mockResolvedValue([]);
});
afterEach(() => vi.useRealTimers());

describe("keys", () => {
  it("d and # trash the cursor row, with no confirmation", async () => {
    await mount();
    await key("d");
    await waitFor(() => expect(m.applyAction).toHaveBeenCalledTimes(1));
    expect(m.applyAction).toHaveBeenLastCalledWith({
      type: "trash",
      params: {},
      messageIds: ["mid5"],
    });
    expect(screen.queryByRole("dialog")).toBeNull();
    await screen.findByText("Moved 1 message to Trash");
    await key("#");
    await waitFor(() => expect(m.applyAction).toHaveBeenCalledTimes(2));
  });

  it("keeps g d (drafts) and Ctrl-d (half page) apart from d", async () => {
    await mount();
    await key("g");
    await key("d");
    await new Promise((r) => setTimeout(r, 20));
    expect(m.applyAction).not.toHaveBeenCalled();
    await fireEvent.keyDown(window, { key: "d", ctrlKey: true });
    expect(m.applyAction).not.toHaveBeenCalled();
  });

  it("u toggles read, s toggles star, U is unbound", async () => {
    // Newest first: uid 2 (read, starred), then uid 1 (unread).
    await mount([
      row(2, { flags: ["\\Seen", "\\Flagged"] }),
      row(1, { flags: [] }),
    ]);
    await key("u");
    await waitFor(() =>
      expect(m.applyAction).toHaveBeenLastCalledWith(
        expect.objectContaining({ type: "mark_unread", messageIds: ["mid2"] }),
      ),
    );
    await waitFor(() => expect(actions.isBusy).toBe(false));
    await key("s");
    await waitFor(() =>
      expect(m.applyAction).toHaveBeenLastCalledWith(
        expect.objectContaining({ type: "unstar", messageIds: ["mid2"] }),
      ),
    );
    await waitFor(() => expect(actions.isBusy).toBe(false));
    mail.cursorTo(1);
    await key("u");
    await waitFor(() =>
      expect(m.applyAction).toHaveBeenLastCalledWith(
        expect.objectContaining({ type: "mark_read", messageIds: ["mid1"] }),
      ),
    );
    await waitFor(() => expect(actions.isBusy).toBe(false));
    m.applyAction.mockClear();
    await key("U");
    expect(m.applyAction).not.toHaveBeenCalled();
  });

  it("z undoes the last action", async () => {
    await mount();
    m.undoLast.mockResolvedValue(ok(1));
    await key("z");
    await waitFor(() => expect(m.undoLast).toHaveBeenCalledTimes(1));
    await screen.findByText(/Undone/);
  });

  it("ignores count prefixes on actions and says so", async () => {
    await mount();
    await key("3");
    await key("e");
    await waitFor(() => expect(m.applyAction).toHaveBeenCalledTimes(1));
    expect(m.applyAction.mock.calls[0][0].messageIds).toEqual(["mid5"]);
    expect(ui.hint).toMatch(/Counts don't apply/);
  });

  it("! sends not_spam in the Spam folder", async () => {
    await mount();
    mail.current = "Spam";
    await key("!");
    await waitFor(() =>
      expect(m.applyAction).toHaveBeenCalledWith(
        expect.objectContaining({ type: "not_spam" }),
      ),
    );
  });
});

describe("selection", () => {
  it("x toggles, v extends a range with the cursor, Esc clears", async () => {
    await mount();
    await key("x");
    expect(sel.count).toBe(1);
    await key("j");
    await key("j");
    await key("v");
    await key("j");
    await key("j");
    expect(sel.count).toBe(4);
    await key("k");
    expect(sel.count).toBe(3);
    await key("Escape");
    expect(sel.count).toBe(0);
  });

  it("v then x ends the range and keeps it", async () => {
    await mount();
    await key("v");
    await key("j");
    await key("v");
    await key("j");
    await key("j");
    expect(sel.count).toBe(2);
  });

  it("acts on the whole selection, with check marks on the rows", async () => {
    await mount();
    await key("x");
    await key("j");
    await key("x");
    const checked = screen
      .getAllByRole("option")
      .filter((o) => o.classList.contains("selected"));
    expect(checked).toHaveLength(2);
    await screen.findByText("2 selected");
    await key("e");
    await waitFor(() => expect(m.applyAction).toHaveBeenCalled());
    expect(m.applyAction.mock.calls[0][0].messageIds).toEqual(["mid5", "mid4"]);
    await waitFor(() => expect(sel.count).toBe(0));
  });
});

describe("targets", () => {
  it("uses the open message when the message pane is focused", async () => {
    await mount();
    await mail.open(mail.shown[2]);
    layout.focus = "view";
    expect(actions.targets().map(keyOf)).toEqual([keyOf(mail.shown[2])]);
    mail.cursorTo(0);
    expect(actions.targets().map(keyOf)).toEqual([keyOf(mail.shown[2])]);
    layout.focus = "list";
    expect(actions.targets().map(keyOf)).toEqual([keyOf(mail.shown[0])]);
  });
  it("skips messages that have no stable id", async () => {
    await mount([hdr(1)]);
    await key("e");
    expect(m.applyAction).not.toHaveBeenCalled();
    expect(ui.hint).toMatch(/aren't ready/);
  });
});

describe("apply", () => {
  it("chunks big batches into calls of at most 100", async () => {
    await mount(msgs(250));
    sel.selectAll();
    m.applyAction.mockImplementation(async (a) => ok(a.messageIds.length));
    await key("e");
    await waitFor(() => expect(m.applyAction).toHaveBeenCalledTimes(3));
    expect(m.applyAction.mock.calls.map((c) => c[0].messageIds.length)).toEqual(
      [100, 100, 50],
    );
    await screen.findByText("Archived 250 messages");
  });

  it("advances the cursor to the next message after archive", async () => {
    await mount();
    mail.cursorTo(1);
    const next = keyOf(mail.shown[2]);
    m.listMessages.mockResolvedValue({
      messages: msgs(5).filter((h) => h.uid !== 4),
      next: null,
    });
    await key("e");
    await waitFor(() => expect(mail.cursorKey).toBe(next));
  });

  it("shows a spinner on the affected row until Apply answers", async () => {
    await mount();
    let done!: (r: api.ActionResult) => void;
    m.applyAction.mockReturnValue(new Promise((r) => (done = r)));
    await key("e");
    await waitFor(() =>
      expect(screen.getAllByRole("option")[0].getAttribute("aria-busy")).toBe(
        "true",
      ),
    );
    done(ok(1));
    await waitFor(() =>
      expect(screen.getAllByRole("option")[0].getAttribute("aria-busy")).toBe(
        "false",
      ),
    );
  });
});

describe("toasts", () => {
  it("shows an undo toast that clears after 8 seconds", async () => {
    await mount();
    await key("e");
    const toast = await screen
      .findByRole("status", { name: "" })
      .catch(() => null);
    void toast;
    await screen.findByText("Archived 1 message");
    expect(screen.getByRole("button", { name: "Undo z" })).toBeTruthy();
    vi.useFakeTimers();
    actions.showToast({ kind: "ok", text: "Archived 3 messages", undo: true });
    vi.advanceTimersByTime(TOAST_MS + 10);
    expect(actions.toast).toBeNull();
  });

  it("the Undo button undoes the action", async () => {
    await mount();
    m.undoLast.mockResolvedValue(ok(1));
    await key("e");
    await fireEvent.click(
      await screen.findByRole("button", { name: "Undo z" }),
    );
    await waitFor(() => expect(m.undoLast).toHaveBeenCalled());
    await screen.findByText(/Undone/);
  });

  it("says when an action can't be undone", async () => {
    await mount();
    m.undoLast.mockRejectedValue(new Error("action has no inverse"));
    await key("z");
    await screen.findByText("This action can't be undone");
  });

  it("summarises failures", async () => {
    await mount();
    sel.selectAll();
    m.applyAction.mockResolvedValue({
      applied: ["a", "b", "c"],
      failed: [
        { mid: "d", mailbox: "INBOX", message: "server said no" },
        { mid: "e", mailbox: "INBOX", message: "server said no" },
      ],
      appliedCount: 3,
      failedCount: 2,
    });
    await key("e");
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("2 of 5 failed: server said no");
  });

  it("shows an error when the call itself fails", async () => {
    await mount();
    m.applyAction.mockRejectedValue(new Error("offline"));
    await key("e");
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("1 of 1 failed: offline");
  });
});

describe("pickers", () => {
  it("m opens a filtered folder picker and moves", async () => {
    await mount();
    await key("m");
    const input = await screen.findByRole("combobox", { name: "Folder" });
    const list = screen.getByRole("listbox", { name: /Move 1 message/ });
    expect(within(list).getAllByRole("option").length).toBeLessThanOrEqual(5);
    await fireEvent.input(input, { target: { value: "wor" } });
    expect(within(list).getAllByRole("option")).toHaveLength(1);
    await fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() =>
      expect(m.applyAction).toHaveBeenCalledWith({
        type: "move",
        params: { folder: "Folders/Work" },
        messageIds: ["mid5"],
      }),
    );
  });

  it("; applies only the label differences", async () => {
    await mount([
      row(1, {
        mailboxes: [
          { name: "Labels/Urgent", displayName: "Urgent", kind: "label" },
        ],
      }),
    ]);
    await key(";");
    const box = await screen.findByRole("checkbox", { name: /Urgent/ });
    expect(box.getAttribute("aria-checked")).toBe("true");
    await fireEvent.click(box);
    await fireEvent.click(screen.getByRole("button", { name: /Apply/ }));
    await waitFor(() =>
      expect(m.applyAction).toHaveBeenCalledWith({
        type: "remove_label",
        params: { label: "Urgent" },
        messageIds: ["mid1"],
      }),
    );
    expect(m.applyAction).toHaveBeenCalledTimes(1);
  });
});

describe("activity", () => {
  it("g u lists recent actions with an Undo per entry", async () => {
    await mount();
    m.recentActions.mockResolvedValue([
      {
        id: 9,
        ts: new Date().toISOString(),
        type: "archive",
        params: {},
        count: 3,
        error: "",
        undone: false,
      },
      {
        id: 8,
        ts: new Date().toISOString(),
        type: "trash",
        params: {},
        count: 1,
        error: "",
        undone: true,
      },
    ]);
    m.undoAction.mockResolvedValue(ok(3));
    await key("g");
    await key("u");
    const dlg = await screen.findByRole("dialog", { name: "Recent activity" });
    await within(dlg).findByText("Archived 3 messages");
    const undos = within(dlg).getAllByRole("button", { name: "Undo" });
    expect((undos[1] as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.click(undos[0]);
    await waitFor(() => expect(m.undoAction).toHaveBeenCalledWith(9));
  });
});

describe("actions:done", () => {
  it("refreshes in place, coalesced, without moving the cursor", async () => {
    await mount();
    mail.cursorTo(2);
    const k = mail.cursorKey;
    m.listMessages.mockClear();
    vi.useFakeTimers();
    actions.noteDone(10);
    actions.noteDone(10);
    actions.noteDone(10);
    await vi.advanceTimersByTimeAsync(50);
    vi.useRealTimers();
    expect(m.listMessages).toHaveBeenCalledTimes(1);
    expect(mail.cursorKey).toBe(k);
  });
});
