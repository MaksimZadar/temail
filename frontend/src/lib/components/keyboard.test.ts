import { beforeEach, describe, expect, it, vi } from "vitest";
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
  onEvent: vi.fn(() => () => {}),
}));

import * as api from "../api";
import App from "../../App.svelte";
import { mail } from "../stores/mail.svelte";
import { layout } from "../stores/layout.svelte";
import { app } from "../stores/app.svelte";
import { ui } from "../stores/ui.svelte";
import { hdr } from "../../test-helpers";

const m = vi.mocked(api);
const key = (k: string, target: Element | Window = window) =>
  fireEvent.keyDown(target, { key: k });

const folders = (n: number) =>
  Array.from({ length: n }, (_, i) => ({
    name: i === 0 ? "INBOX" : `Folders/F${i}`,
    displayName: i === 0 ? "Inbox" : `Folder ${i}`,
    kind: (i === 0 ? "inbox" : "folder") as api.MailboxKind,
    uidValidity: 7,
    messages: 1,
    bodies: 0,
    lastSync: null,
  }));

const html = (o: Partial<api.Message> = {}): api.Message => ({
  ...hdr(1),
  to: null,
  textBody: "line one\nline two",
  htmlBody: "<p>Hello <b>there</b></p>",
  attachments: null,
  ...o,
});

async function mount(nFolders = 1, message = html()) {
  app.route = "main";
  ui.overlay = "none";
  mail.current = null;
  mail.folders = [];
  mail.foldersLoaded = false;
  mail.messages = [];
  mail.next = null;
  mail.cursorKey = null;
  mail.loading = false;
  mail.error = "";
  mail.filter = "";
  mail.message = null;
  mail.messageError = "";
  mail.messageLoading = false;
  mail.openKey = null;
  mail.plain = false;
  layout.focus = "list";
  layout.folderVisible = true;
  layout.width = 1280;
  m.listMailboxes.mockResolvedValue(folders(nFolders));
  m.listMessages.mockResolvedValue({ messages: [hdr(1)], next: null });
  m.getMessage.mockResolvedValue(message);
  render(App);
  await screen.findAllByRole("option");
  await waitFor(() => expect(mail.cursorIndex).toBe(0));
}

beforeEach(() => vi.clearAllMocks());

describe("help overlay keys", () => {
  it("closes with Esc, q and ? while focus is inside it", async () => {
    await mount();
    for (const k of ["Escape", "q", "?"]) {
      await key("?");
      const dialog = await screen.findByRole("dialog", {
        name: "Keyboard shortcuts",
      });
      // Focus is inside the dialog, as it is for a real user.
      await waitFor(() =>
        expect(dialog.contains(document.activeElement)).toBe(true),
      );
      await key(k, document.activeElement as Element);
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    }
  });

  it("closes the sync panel with Esc and q", async () => {
    await mount();
    for (const k of ["Escape", "q"]) {
      ui.overlay = "sync";
      await screen.findByRole("dialog", { name: "Sync status" });
      await key(k);
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    }
  });

  it("keeps Tab inside the dialog", async () => {
    await mount();
    await key("?");
    const dialog = await screen.findByRole("dialog");
    const close = screen.getByRole("button", { name: /Close/ });
    close.focus();
    await fireEvent.keyDown(close, { key: "Tab" });
    expect(dialog.contains(document.activeElement)).toBe(true);
  });
});

describe("command palette", () => {
  const options = () =>
    within(
      screen.getByRole("listbox", { name: "Matching commands" }),
    ).queryAllByRole("option");
  async function open() {
    await key(":");
    return screen.findByRole("combobox", { name: "Command" });
  }

  it("shows at most 5 options even with many commands", async () => {
    await mount(30);
    await open();
    expect(options().length).toBeLessThanOrEqual(5);
    expect(options()).toHaveLength(5);
  });

  it("filters as you type and highlights the match", async () => {
    await mount(30);
    const input = await open();
    await fireEvent.input(input, { target: { value: "folder 7" } });
    const opts = options();
    expect(opts.length).toBeLessThanOrEqual(5);
    // The exact match is first; looser in-order matches (17, 27) follow.
    expect(opts[0]).toHaveTextContent("Go to Folder 7");
    expect(opts[0].querySelector("mark")).not.toBeNull();
  });

  it("scrolls the 5-row window with the arrow keys", async () => {
    await mount(30);
    const input = await open();
    for (let i = 0; i < 7; i++)
      await fireEvent.keyDown(input, { key: "ArrowDown" });
    const opts = options();
    expect(opts).toHaveLength(5);
    expect(opts.some((o) => o.getAttribute("aria-selected") === "true")).toBe(
      true,
    );
    expect(input.getAttribute("aria-activedescendant")).toBe("cmd-7");
    await fireEvent.keyDown(input, { key: "n", ctrlKey: true });
    expect(input.getAttribute("aria-activedescendant")).toBe("cmd-8");
  });

  it("Enter runs the highlighted command", async () => {
    await mount(3);
    const input = await open();
    await fireEvent.input(input, { target: { value: "folder 2" } });
    await fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(mail.current).toBe("Folders/F2"));
    expect(screen.queryByRole("combobox")).toBeNull();
  });

  it("a click runs a command", async () => {
    await mount(3);
    const input = await open();
    await fireEvent.input(input, { target: { value: "dark" } });
    await fireEvent.click(
      screen
        .getByRole("option", { name: /Theme: dark/ })
        .querySelector("button")!,
    );
    expect(document.documentElement.dataset.theme).toBe("dark");
    ui.setTheme("auto");
  });

  it("shows an empty state, and Esc and typing q behave", async () => {
    await mount();
    const input = await open();
    await fireEvent.input(input, { target: { value: "qqqq" } });
    expect(screen.getByText("No matching commands")).toBeInTheDocument();
    // q in the input is just text: the palette stays open.
    await key("q", input);
    expect(screen.getByRole("combobox")).toBeInTheDocument();
    await key("Escape", input);
    await waitFor(() => expect(screen.queryByRole("combobox")).toBeNull());
  });
});

describe("message pane keyboard", () => {
  it("l opens the message without putting focus in the frame, and h gets back out", async () => {
    await mount();
    await key("Enter"); // open: focus moves to the message pane
    await screen.findByTitle("Message body");
    expect(layout.focus).toBe("view");
    await key("l"); // l again must not enter the frame either
    expect(document.activeElement?.tagName).not.toBe("IFRAME");
    expect(screen.getByTitle("Message body")).toHaveAttribute("tabindex", "-1");
    await key("h", document.activeElement ?? window);
    expect(layout.focus).toBe("list");
    expect(document.activeElement).toHaveAttribute("role", "listbox");
  });

  it("Esc also leaves the message pane", async () => {
    await mount();
    await key("Enter");
    await screen.findByTitle("Message body");
    await key("Escape");
    expect(layout.focus).toBe("list");
  });

  it("offers a way back if the frame does get the keyboard", async () => {
    await mount();
    await key("Enter");
    const frame = (await screen.findByTitle(
      "Message body",
    )) as HTMLIFrameElement;
    frame.focus(); // a click inside the frame does this
    expect(document.activeElement).toBe(frame);
    await fireEvent.focusIn(frame);
    const back = await screen.findByRole("button", { name: "Back to keys" });
    await fireEvent.click(back);
    expect(document.activeElement).not.toBe(frame);
    expect(screen.queryByRole("button", { name: "Back to keys" })).toBeNull();
    await key("h");
    expect(layout.focus).toBe("list");
  });

  it("j/k, Ctrl-d/u/f/b and gg/G scroll the message when its pane is focused", async () => {
    await mount();
    await key("Enter");
    await screen.findByTitle("Message body");
    // The first scroll key switches to plain text, which the app can scroll.
    await key("j");
    const pre = await screen.findByLabelText("Message text");
    expect(screen.queryByTitle("Message body")).toBeNull();
    Object.defineProperty(pre, "clientHeight", {
      value: 400,
      configurable: true,
    });
    Object.defineProperty(pre, "scrollHeight", {
      value: 5000,
      configurable: true,
    });
    const spy = vi.fn();
    pre.scrollBy = spy as unknown as typeof pre.scrollBy;
    await key("j");
    expect(spy).toHaveBeenLastCalledWith({ top: 48 });
    await key("3");
    await key("k");
    expect(spy).toHaveBeenLastCalledWith({ top: -144 });
    await key("d", document.body);
    await fireEvent.keyDown(window, { key: "d", ctrlKey: true });
    expect(spy).toHaveBeenLastCalledWith({ top: 200 });
    await fireEvent.keyDown(window, { key: "u", ctrlKey: true });
    expect(spy).toHaveBeenLastCalledWith({ top: -200 });
    await fireEvent.keyDown(window, { key: "f", ctrlKey: true });
    expect(spy).toHaveBeenLastCalledWith({ top: 360 });
    await fireEvent.keyDown(window, { key: "b", ctrlKey: true });
    expect(spy).toHaveBeenLastCalledWith({ top: -360 });
    await key("G");
    expect(pre.scrollTop).toBe(5000);
    await key("g");
    await key("g");
    expect(pre.scrollTop).toBe(0);
    // The message is still scrollable and the list is not what moved.
    expect(mail.cursorIndex).toBe(0);
  });

  it("j and k still move the list when the list is focused", async () => {
    app.route = "main";
    await mount();
    m.listMessages.mockResolvedValue({
      messages: [hdr(2), hdr(1)],
      next: null,
    });
    await mail.reload();
    await waitFor(() => expect(mail.shown).toHaveLength(2));
    layout.focus = "list";
    const before = mail.cursorIndex;
    await key("j");
    expect(mail.cursorIndex).toBe(before + 1);
  });
});
