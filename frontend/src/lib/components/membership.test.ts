import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/svelte";

const handlers = vi.hoisted(() => ({}) as Record<string, (d: unknown) => void>);

vi.mock("../api", () => ({
  listMailboxes: vi.fn(async () => []),
  listMessages: vi.fn(async () => ({ messages: [], next: null })),
  getMessage: vi.fn(),
  getAttachment: vi.fn(),
  openUrl: vi.fn(),
  cacheInfo: vi.fn(async () => ({ bytes: 0, messages: 0, bodies: 0 })),
  syncStart: vi.fn(async () => {}),
  syncStatus: vi.fn(async () => ({
    running: false,
    watching: true,
    idle: true,
    lastChecked: null,
    stopTimedOut: false,
    lastResult: null,
    lastError: "",
  })),
  onEvent: vi.fn((name: string, fn: (d: unknown) => void) => {
    handlers[name] = fn;
    return () => delete handlers[name];
  }),
}));

import FolderPane from "./FolderPane.svelte";
import MessagePane from "./MessagePane.svelte";
import MessageList from "./MessageList.svelte";
import Footer from "./Footer.svelte";
import MainView from "./MainView.svelte";
import * as api from "../api";
import { mail } from "../stores/mail.svelte";
import { layout } from "../stores/layout.svelte";
import { sync } from "../stores/sync.svelte";
import { app } from "../stores/app.svelte";
import { hdr } from "../../test-helpers";
import type { MailboxDTO, MailboxRef, Message } from "../api";

const box = (
  name: string,
  kind: MailboxDTO["kind"],
  unseen: number,
  messages = 10,
): MailboxDTO => ({
  name,
  displayName: name === "INBOX" ? "Inbox" : name,
  kind,
  uidValidity: 1,
  messages,
  bodies: 0,
  unseen,
  lastSync: null,
});
const refs: MailboxRef[] = [
  { name: "INBOX", displayName: "Inbox", kind: "inbox" },
  { name: "Labels/Work", displayName: "Work", kind: "label" },
  { name: "Labels/Invoices", displayName: "Invoices", kind: "label" },
];
const full = (): Message => ({
  ...hdr(1),
  mailboxes: refs,
  to: null,
  textBody: "hello",
  htmlBody: "",
  attachments: null,
});

beforeEach(() => {
  mail.folders = [];
  mail.foldersLoaded = true;
  mail.current = "INBOX";
  mail.messages = [];
  mail.message = null;
  mail.openGone = false;
  layout.focus = "list";
  layout.width = 1280;
  layout.listW = 560;
  sync.reset();
  app.route = "main";
});

describe("unread counts in the folder pane", () => {
  it("shows a badge and bold name when there is unread mail, nothing at 0", () => {
    mail.folders = [box("INBOX", "inbox", 3, 120), box("Sent", "sent", 0, 40)];
    render(FolderPane);
    const inbox = screen.getByRole("button", { name: /Inbox/ });
    expect(inbox).toHaveTextContent("3");
    expect(inbox.className).toMatch(/unread/);
    expect(inbox.getAttribute("title")).toBe("3 unread · 120 messages");
    expect(inbox.getAttribute("aria-label")).toBe(
      "Inbox, 3 unread, 120 messages",
    );
    const sent = screen.getByRole("button", { name: /Sent/ });
    expect(sent.className).not.toMatch(/unread/);
    expect(sent.querySelector(".badge")).toBeNull();
    expect(sent.getAttribute("aria-label")).toBe("Sent, 40 messages");
  });

  it("Drafts shows how many drafts there are, not an unread badge", () => {
    mail.folders = [box("Drafts", "drafts", 2, 5)];
    render(FolderPane);
    const d = screen.getByRole("button", { name: /Drafts/ });
    expect(d.querySelector(".badge")).toBeNull();
    expect(d.querySelector(".count")).toHaveTextContent("5");
  });

  it("updates when the counts change", async () => {
    mail.folders = [box("INBOX", "inbox", 3)];
    render(FolderPane);
    expect(screen.getByRole("button", { name: /Inbox/ })).toHaveTextContent(
      "3",
    );
    mail.folders = [box("INBOX", "inbox", 0)];
    await Promise.resolve();
    expect(
      screen.getByRole("button", { name: /Inbox/ }).querySelector(".badge"),
    ).toBeNull();
  });
});

describe("where a message lives", () => {
  it("shows a folder chip and label chips with tooltips", () => {
    mail.message = full();
    mail.openKey = "k";
    render(MessagePane);
    const inbox = screen.getByRole("button", { name: "Inbox" });
    expect(inbox).toHaveAttribute("title", "Go to Inbox");
    const work = screen.getByRole("button", { name: "Label Work" });
    expect(work).toHaveAttribute("title", "Show messages labelled Work");
    expect(
      screen.getByRole("button", { name: "Label Invoices" }),
    ).toBeInTheDocument();
  });

  it("clicking a chip jumps to that folder or label", async () => {
    mail.message = full();
    mail.openKey = "k";
    const go = vi.spyOn(mail, "goto").mockResolvedValue();
    render(MessagePane);
    await fireEvent.click(screen.getByRole("button", { name: "Label Work" }));
    expect(go).toHaveBeenCalledWith("Labels/Work");
    await fireEvent.click(screen.getByRole("button", { name: "Inbox" }));
    expect(go).toHaveBeenLastCalledWith("INBOX");
    go.mockRestore();
  });

  it("shows no chip row for a message without membership", () => {
    mail.message = { ...full(), mailboxes: null };
    mail.openKey = "k";
    render(MessagePane);
    expect(screen.queryByText("In")).toBeNull();
  });

  it("says when the open message is no longer in the folder", () => {
    mail.message = full();
    mail.openKey = "k";
    mail.openGone = true;
    render(MessagePane);
    expect(screen.getByText(/no longer in this folder/)).toBeInTheDocument();
  });
});

describe("label chips in the list", () => {
  const withLabels = () => hdr(1, { mailboxes: refs });
  it("shows up to two compact label chips on a wide list, not the current folder", () => {
    mail.current = "INBOX";
    mail.messages = [
      hdr(1, {
        mailboxes: [
          ...refs,
          { name: "Labels/C", displayName: "C", kind: "label" },
        ],
      }),
    ];
    render(MessageList);
    const row = screen.getByRole("option");
    expect(row).toHaveTextContent("Work");
    expect(row).toHaveTextContent("Invoices");
    expect(row).toHaveTextContent("+1");
  });

  it("hides them when the list is narrow", () => {
    layout.listW = 400;
    mail.messages = [withLabels()];
    render(MessageList);
    expect(screen.getByRole("option").querySelector(".labels")).toBeNull();
  });

  it("doesn't repeat the label you are looking at", () => {
    mail.current = "Labels/Work";
    mail.messages = [withLabels()];
    render(MessageList);
    const row = screen.getByRole("option");
    expect(within(row).queryByTitle("Work")).toBeNull();
    expect(within(row).getByTitle("Invoices")).toBeInTheDocument();
  });
});

describe("Sync now in the sync panel and palette", () => {
  it("the panel has a Sync now button that asks for a check", async () => {
    const m = vi.mocked(api);
    const SyncPanel = (await import("./SyncPanel.svelte")).default;
    sync.watching = true;
    sync.idle = true;
    render(SyncPanel);
    await fireEvent.click(screen.getByRole("button", { name: "Sync now" }));
    expect(m.syncStart).toHaveBeenCalledTimes(1);
  });

  it("the button is off during the first sync", async () => {
    const SyncPanel = (await import("./SyncPanel.svelte")).default;
    sync.running = true;
    render(SyncPanel);
    expect(screen.getByRole("button", { name: "Sync now" })).toBeDisabled();
  });

  it("the palette has Sync now", async () => {
    const Palette = (await import("./Palette.svelte")).default;
    const m = vi.mocked(api);
    render(Palette);
    const input = screen.getByRole("combobox", { name: "Command" });
    await fireEvent.input(input, { target: { value: "sync now" } });
    expect(screen.getAllByRole("option")[0]).toHaveTextContent("Sync now");
    await fireEvent.keyDown(input, { key: "Enter" });
    expect(m.syncStart).toHaveBeenCalled();
  });
});

describe("footer", () => {
  it("shows Live with no spinner, then a polling hint when IDLE is down", async () => {
    sync.watching = true;
    sync.idle = true;
    render(Footer);
    expect(screen.getByText("Live")).toBeInTheDocument();
    expect(screen.queryByText("polling")).toBeNull();
    sync.idle = false;
    sync.lastChecked = Date.now() - 2 * 60_000;
    sync.now = Date.now();
    expect(await screen.findByText("polling")).toBeInTheDocument();
    expect(
      screen.getByText(/Up to date · checked 2 min ago/),
    ).toBeInTheDocument();
    const dot = document.querySelector(".dot")!;
    expect(dot.className).not.toMatch(/busy/);
  });
});

describe("the mailbox view listens for live changes", () => {
  it("sync:changed refreshes the counts after a short coalescing delay", async () => {
    vi.useFakeTimers();
    try {
      const m = vi.mocked(api);
      m.listMailboxes.mockResolvedValue([box("INBOX", "inbox", 1)]);
      render(MainView);
      await vi.advanceTimersByTimeAsync(10);
      m.listMailboxes.mockClear();
      m.listMailboxes.mockResolvedValue([box("INBOX", "inbox", 5)]);
      handlers["sync:changed"]({ mailboxes: ["INBOX"] });
      handlers["sync:changed"]({ mailboxes: ["INBOX"] });
      await vi.advanceTimersByTimeAsync(100);
      expect(m.listMailboxes).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(300);
      expect(m.listMailboxes).toHaveBeenCalledTimes(1);
      expect(mail.folders[0].unseen).toBe(5);
    } finally {
      vi.useRealTimers();
    }
  });
});
