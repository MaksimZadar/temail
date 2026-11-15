import { beforeEach, describe, expect, it, vi } from "vitest";
import type { MailboxDTO, MessageHeader } from "../api";

vi.mock("../api", () => ({
  listMailboxes: vi.fn(),
  listMessages: vi.fn(),
  getMessage: vi.fn(),
  getAttachment: vi.fn(),
  openUrl: vi.fn(),
  onEvent: vi.fn(() => () => {}),
}));

import * as api from "../api";
import { Mail, PAGE } from "./mail.svelte";
import { keyOf } from "../mail/format";
import { hdr } from "../../test-helpers";

const m = vi.mocked(api);
const box = (name: string, kind: MailboxDTO["kind"]): MailboxDTO => ({
  name,
  displayName: name,
  kind,
  uidValidity: 7,
  messages: 3,
  bodies: 0,
  lastSync: null,
});
const page = (uids: number[], next = false) => ({
  messages: uids.map((u) => hdr(u)),
  next: next ? { date: "2026-01-01T00:00:00Z", uid: uids.at(-1)! } : null,
});

let mail: Mail;
beforeEach(() => {
  vi.resetAllMocks();
  m.listMailboxes.mockResolvedValue([
    box("Sent", "sent"),
    box("INBOX", "inbox"),
  ]);
  m.listMessages.mockResolvedValue(page([5, 4, 3, 2, 1]));
  mail = new Mail();
});

describe("Mail store", () => {
  it("loads folders in order and opens the Inbox", async () => {
    await mail.loadFolders();
    expect(mail.folders.map((f) => f.name)).toEqual(["INBOX", "Sent"]);
    expect(mail.current).toBe("INBOX");
    expect(mail.messages).toHaveLength(5);
    expect(m.listMessages).toHaveBeenCalledWith("INBOX", null, PAGE);
    expect(mail.cursorIndex).toBe(0);
  });

  it("pages with the cursor and appends", async () => {
    m.listMessages.mockResolvedValueOnce(page([5, 4], true));
    m.listMessages.mockResolvedValueOnce(page([3, 2], false));
    await mail.loadFolders();
    await mail.loadPage(false);
    expect(mail.messages.map((x) => x.uid)).toEqual([5, 4, 3, 2]);
    expect(mail.next).toBeNull();
    expect(m.listMessages).toHaveBeenLastCalledWith(
      "INBOX",
      expect.objectContaining({ uid: 4 }),
      PAGE,
    );
  });

  it("keeps the cursor on the same message when new mail arrives", async () => {
    await mail.loadFolders();
    mail.cursorTo(2); // uid 3
    const before = keyOf(mail.shown[mail.cursorIndex]);
    m.listMessages.mockResolvedValueOnce(page([8, 7, 6, 5, 4]));
    await mail.refreshHead();
    expect(mail.messages.map((x) => x.uid)).toEqual([8, 7, 6, 5, 4, 3, 2, 1]);
    expect(keyOf(mail.shown[mail.cursorIndex])).toBe(before);
    expect(mail.cursorIndex).toBe(5);
  });

  it("keeps the open message while the list refreshes", async () => {
    await mail.loadFolders();
    m.getMessage.mockResolvedValue({
      ...hdr(3),
      textBody: "hi",
      htmlBody: "",
      attachments: null,
    });
    await mail.open(mail.messages[2]);
    m.listMessages.mockResolvedValueOnce(page([9, 8, 7, 6, 5]));
    await mail.refreshHead();
    expect(mail.message?.uid).toBe(3);
    expect(mail.openKey).toBe(keyOf(hdr(3)));
  });

  it("filters loaded messages and keeps the cursor valid", async () => {
    await mail.loadFolders();
    mail.cursorTo(3);
    mail.setFilter("Subject 2");
    expect(mail.shown.map((x) => x.uid)).toEqual([2]);
    expect(mail.cursorIndex).toBe(0);
    mail.setFilter("");
    expect(mail.shown).toHaveLength(5);
  });

  it("moves the cursor with clamping", async () => {
    await mail.loadFolders();
    mail.moveCursor(-1);
    expect(mail.cursorIndex).toBe(0);
    mail.moveCursor(3);
    expect(mail.cursorIndex).toBe(3);
    mail.moveCursor(100);
    expect(mail.cursorIndex).toBe(4);
  });

  it("G walks to the real end when more pages exist", async () => {
    m.listMessages.mockResolvedValueOnce(page([5, 4], true));
    m.listMessages.mockResolvedValueOnce(page([3, 2], true));
    m.listMessages.mockResolvedValueOnce(page([1], false));
    await mail.loadFolders();
    await mail.cursorToEnd();
    expect(mail.shown.at(-1)!.uid).toBe(1);
    expect(mail.cursorIndex).toBe(4);
  });

  it("ignores a late page from a folder you already left", async () => {
    await mail.loadFolders();
    let release!: (p: ReturnType<typeof page>) => void;
    m.listMessages.mockReturnValueOnce(new Promise((r) => (release = r)));
    const slow = mail.selectFolder("Sent");
    m.listMessages.mockResolvedValueOnce(page([42]));
    release(page([99]));
    await slow;
    expect(mail.current).toBe("Sent");
    expect(mail.messages.map((x) => x.uid)).not.toContain(1);
  });

  it("shows a friendly message and reloads on a stale epoch", async () => {
    await mail.loadFolders();
    m.getMessage.mockRejectedValue(
      new Error(
        "the mailbox was reset on the server; sync again before reading it",
      ),
    );
    await mail.open(mail.messages[0] as MessageHeader);
    expect(mail.messageError).toMatch(/reset on the server/);
    expect(m.listMessages.mock.calls.length).toBeGreaterThan(1);
  });

  it("reports a list error", async () => {
    m.listMessages.mockRejectedValue(new Error("db locked"));
    await mail.loadFolders();
    expect(mail.error).toBe("db locked");
  });
});
