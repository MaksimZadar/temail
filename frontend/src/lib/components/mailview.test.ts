import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/svelte";

vi.mock("../api", () => ({
  listMailboxes: vi.fn(),
  listMessages: vi.fn(),
  getMessage: vi.fn(),
  getAttachment: vi.fn(),
  openUrl: vi.fn(async () => {}),
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
import MessagePane from "./MessagePane.svelte";
import MessageList from "./MessageList.svelte";
import App from "../../App.svelte";
import { mail } from "../stores/mail.svelte";
import { layout } from "../stores/layout.svelte";
import { app } from "../stores/app.svelte";
import { ui } from "../stores/ui.svelte";
import { hdr } from "../../test-helpers";

const m = vi.mocked(api);

const full = (o: Partial<api.Message> = {}): api.Message => ({
  ...hdr(1),
  to: [{ name: "Me", email: "me@proton.me" }],
  textBody: "plain words",
  htmlBody:
    '<p>Hello <a href="https://evil.example/x">paypal.com</a> <a href="https://ok.example/">Read more</a></p>',
  attachments: [
    {
      filename: "a.pdf",
      contentType: "application/pdf",
      size: 2048,
      partId: "2",
    },
  ],
  ...o,
});

function setMessage(msg: api.Message | null): void {
  mail.message = msg;
  mail.openKey = msg ? "k" : null;
  mail.messageLoading = false;
  mail.messageError = "";
}

beforeEach(() => {
  vi.clearAllMocks();
  mail.messages = [];
  mail.message = null;
  mail.plain = false;
  mail.filter = "";
  mail.cursorKey = null;
  mail.current = null;
  mail.folders = [];
  layout.focus = "list";
  layout.width = 1280;
});

describe("MessagePane", () => {
  it("sandboxes the body: no scripts, no same-origin, strict CSP", () => {
    setMessage(full());
    render(MessagePane);
    const frame = screen.getByTitle("Message body") as HTMLIFrameElement;
    expect(frame.getAttribute("sandbox")).toBe("");
    expect(frame.getAttribute("sandbox")).not.toMatch(/allow-/);
    expect(frame.getAttribute("srcdoc")).toContain(
      "default-src 'none'; style-src 'unsafe-inline'; img-src data:",
    );
    expect(frame.getAttribute("srcdoc")).not.toMatch(/href=/);
    expect(frame.getAttribute("referrerpolicy")).toBe("no-referrer");
  });

  it("says remote images are blocked, with no load button", () => {
    setMessage(full());
    render(MessagePane);
    expect(screen.getByText("Remote images are blocked.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /load images/i })).toBeNull();
  });

  it("switches to plain text and back", async () => {
    setMessage(full());
    render(MessagePane);
    await fireEvent.click(screen.getByRole("button", { name: "Plain text" }));
    await waitFor(() =>
      expect(screen.getByText("plain words")).toBeInTheDocument(),
    );
    expect(screen.queryByTitle("Message body")).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "Formatted" }));
    expect(screen.getByTitle("Message body")).toBeInTheDocument();
  });

  it("falls back to plain text when there is no html", () => {
    setMessage(full({ htmlBody: "" }));
    render(MessagePane);
    expect(screen.getByText("plain words")).toBeInTheDocument();
    expect(screen.queryByTitle("Message body")).toBeNull();
  });

  it("opens a plain link in the system browser without asking", async () => {
    setMessage(full());
    render(MessagePane);
    await fireEvent.click(screen.getByRole("button", { name: /Links \(2\)/ }));
    await fireEvent.click(screen.getByRole("button", { name: /Read more/ }));
    expect(m.openUrl).toHaveBeenCalledWith("https://ok.example/");
  });

  it("asks first when the link text shows a different address", async () => {
    setMessage(full());
    render(MessagePane);
    await fireEvent.click(screen.getByRole("button", { name: /Links \(2\)/ }));
    await fireEvent.click(screen.getByRole("button", { name: /paypal\.com/ }));
    expect(m.openUrl).not.toHaveBeenCalled();
    expect(screen.getByRole("alertdialog")).toHaveTextContent("evil.example");
    await fireEvent.click(
      screen.getByRole("button", { name: "Open in browser" }),
    );
    expect(m.openUrl).toHaveBeenCalledWith("https://evil.example/x");
  });

  it("lists attachment metadata and downloads through GetAttachment", async () => {
    setMessage(full());
    m.getAttachment.mockResolvedValue({
      filename: "a.pdf",
      contentType: "application/pdf",
      size: 3,
      data: btoa("abc"),
    });
    URL.createObjectURL = vi.fn(() => "blob:x");
    URL.revokeObjectURL = vi.fn();
    render(MessagePane);
    expect(screen.getByText("a.pdf")).toBeInTheDocument();
    expect(screen.getByText(/application\/pdf · 2\.0 KB/)).toBeInTheDocument();
    await fireEvent.click(
      screen.getByRole("button", { name: "Download a.pdf" }),
    );
    await waitFor(() =>
      expect(m.getAttachment).toHaveBeenCalledWith("INBOX", 7, 1, "2"),
    );
    await waitFor(() => expect(URL.createObjectURL).toHaveBeenCalled());
  });

  it("shows a friendly attachment error", async () => {
    setMessage(full());
    m.getAttachment.mockRejectedValue(new Error("attachment too large"));
    render(MessagePane);
    await fireEvent.click(
      screen.getByRole("button", { name: "Download a.pdf" }),
    );
    expect(
      await screen.findByText("Too large to download here."),
    ).toBeInTheDocument();
  });

  it("shows empty and error states", () => {
    setMessage(null);
    const { unmount } = render(MessagePane);
    expect(
      screen.getByText("Select a message to read it."),
    ).toBeInTheDocument();
    unmount();
    mail.messageError = "boom";
    render(MessagePane);
    expect(screen.getByRole("alert")).toHaveTextContent("boom");
  });
});

describe("MessageList virtualization", () => {
  it("renders a window of rows, not 100k", () => {
    mail.current = "INBOX";
    mail.messages = Array.from({ length: 100_000 }, (_, i) => hdr(100_000 - i));
    render(MessageList);
    const rows = screen.getAllByRole("option");
    expect(rows.length).toBeGreaterThan(5);
    expect(rows.length).toBeLessThan(80);
    expect(rows[0]).toHaveAttribute("aria-setsize", "100000");
    expect(rows[0]).toHaveAttribute("aria-posinset", "1");
  });

  it("marks unread rows and exposes the cursor to assistive tech", async () => {
    mail.current = "INBOX";
    mail.messages = [hdr(2), hdr(1, { flags: ["\\Seen"] })];
    mail.cursorKey = `INBOX\u00007\u00002`;
    render(MessageList);
    const rows = screen.getAllByRole("option");
    expect(rows[0].className).toMatch(/unread/);
    expect(rows[1].className).not.toMatch(/unread/);
    expect(rows[0]).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("listbox")).toHaveAttribute(
      "aria-activedescendant",
      "msg-0",
    );
  });

  it("shows empty, loading and filter-miss states", () => {
    mail.current = "INBOX";
    const { unmount } = render(MessageList);
    expect(screen.getByText("This folder is empty.")).toBeInTheDocument();
    unmount();
    mail.messages = [hdr(1)];
    mail.filter = "nomatch";
    render(MessageList);
    expect(screen.getByText(/No messages match/)).toBeInTheDocument();
  });

  it("clicking a row opens it", async () => {
    mail.current = "INBOX";
    mail.messages = [hdr(2), hdr(1)];
    m.getMessage.mockResolvedValue(full());
    render(MessageList);
    await fireEvent.click(screen.getAllByRole("option")[1]);
    expect(m.getMessage).toHaveBeenCalledWith("INBOX", 7, 1);
  });
});

describe("keyboard in the mailbox view", () => {
  it("j/k, counts, G and gg move the cursor; Enter opens", async () => {
    app.route = "main";
    ui.overlay = "none";
    m.listMailboxes.mockResolvedValue([
      {
        name: "INBOX",
        displayName: "Inbox",
        kind: "inbox",
        uidValidity: 7,
        messages: 5,
        bodies: 0,
        lastSync: null,
      },
    ]);
    m.listMessages.mockResolvedValue({
      messages: [5, 4, 3, 2, 1].map((u) => hdr(u)),
      next: null,
    });
    m.getMessage.mockResolvedValue(full());
    render(App);
    await screen.findAllByRole("option");
    const press = (key: string) => fireEvent.keyDown(window, { key });
    await waitFor(() => expect(mail.cursorIndex).toBe(0));
    await press("j");
    expect(mail.cursorIndex).toBe(1);
    await press("2");
    await press("j");
    expect(mail.cursorIndex).toBe(3);
    await press("k");
    expect(mail.cursorIndex).toBe(2);
    await press("G");
    await waitFor(() => expect(mail.cursorIndex).toBe(4));
    await press("g");
    await press("g");
    expect(mail.cursorIndex).toBe(0);
    await press("Enter");
    await waitFor(() =>
      expect(m.getMessage).toHaveBeenCalledWith("INBOX", 7, 5),
    );
    expect(layout.focus).toBe("view");
    await press("h");
    expect(layout.focus).toBe("list");
  });

  it("/ focuses the filter and Esc clears it", async () => {
    app.route = "main";
    ui.overlay = "none";
    m.listMailboxes.mockResolvedValue([]);
    render(App);
    const input = await screen.findByPlaceholderText("Filter this folder");
    await waitFor(() =>
      expect(document.activeElement).toHaveAttribute("role", "listbox"),
    );
    await fireEvent.keyDown(window, { key: "/" });
    expect(document.activeElement).toBe(input);
    await fireEvent.input(input, { target: { value: "abc" } });
    expect(mail.filter).toBe("abc");
    await fireEvent.keyDown(input, { key: "Escape" });
    expect(mail.filter).toBe("");
  });
});

describe("focused pane highlight", () => {
  const pane = (name: string) =>
    document.querySelector<HTMLElement>(`[data-pane="${name}"]`)!;

  async function mount() {
    app.route = "main";
    ui.overlay = "none";
    m.listMailboxes.mockResolvedValue([
      {
        name: "INBOX",
        displayName: "Inbox",
        kind: "inbox",
        uidValidity: 7,
        messages: 1,
        bodies: 0,
        lastSync: null,
      },
    ]);
    m.listMessages.mockResolvedValue({ messages: [hdr(1)], next: null });
    m.getMessage.mockResolvedValue(full());
    layout.folderVisible = true;
    render(App);
    await screen.findAllByRole("option");
  }

  it("marks exactly one pane as focused and follows h/l", async () => {
    await mount();
    const focusedPanes = () =>
      ["folders", "list", "view"].filter(
        (n) => pane(n).dataset.focused === "true",
      );
    await waitFor(() => expect(focusedPanes()).toEqual(["list"]));
    expect(pane("list")).toHaveClass("focused");
    await fireEvent.keyDown(window, { key: "h" });
    expect(focusedPanes()).toEqual(["folders"]);
    expect(pane("folders")).toHaveClass("focused");
    await fireEvent.keyDown(window, { key: "l" });
    expect(focusedPanes()).toEqual(["list"]);
    await fireEvent.keyDown(window, { key: "l" });
    await waitFor(() => expect(focusedPanes()).toEqual(["view"]));
  });

  it("follows mouse clicks and focus-within", async () => {
    await mount();
    await fireEvent.pointerDown(pane("folders"));
    expect(layout.focus).toBe("folders");
    expect(pane("folders").dataset.focused).toBe("true");
    expect(pane("list").dataset.focused).toBe("false");
    await fireEvent.focusIn(pane("view"));
    expect(layout.focus).toBe("view");
    expect(pane("view")).toHaveClass("focused");
  });

  it("clicking a folder keeps the highlight on the folder pane", async () => {
    await mount();
    const btn = screen.getByRole("button", { name: /Inbox/ });
    await fireEvent.pointerDown(btn);
    await fireEvent.click(btn);
    expect(layout.focus).toBe("folders");
  });
});
