import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import {
  render,
  screen,
  fireEvent,
  waitFor,
  within,
  cleanup,
} from "@testing-library/svelte";

const handlers = vi.hoisted(() => new Map<string, ((d: unknown) => void)[]>());

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
  undoAction: vi.fn(async () => ({
    applied: [],
    failed: null,
    appliedCount: 1,
    failedCount: 0,
  })),
  undoLast: vi.fn(),
  recentActions: vi.fn(async () => []),
  listProviders: vi.fn(),
  suggest: vi.fn(),
  suggestStatus: vi.fn(),
  suggestCancel: vi.fn(async () => {}),
  applySuggestions: vi.fn(),
  onEvent: vi.fn((name: string, fn: (d: unknown) => void) => {
    handlers.set(name, [...(handlers.get(name) ?? []), fn]);
    return () =>
      handlers.set(
        name,
        (handlers.get(name) ?? []).filter((f) => f !== fn),
      );
  }),
}));

import * as api from "../api";
import App from "../../App.svelte";
import { mail } from "../stores/mail.svelte";
import { layout } from "../stores/layout.svelte";
import { app } from "../stores/app.svelte";
import { ui } from "../stores/ui.svelte";
import { sel } from "../stores/select.svelte";
import { actions } from "../stores/actions.svelte";
import { suggest } from "../stores/suggest.svelte";
import { keyOf } from "../mail/format";
import { hdr } from "../../test-helpers";

const m = vi.mocked(api);
const key = (k: string, target: Element | Window = window) =>
  fireEvent.keyDown(target, { key: k });
const emit = (name: string, data: unknown) =>
  (handlers.get(name) ?? []).forEach((f) => f(data));

const provider = (o: Partial<api.LLMProvider> = {}): api.LLMProvider => ({
  name: "ollama",
  baseURL: "http://127.0.0.1:11434/v1",
  model: "llama3",
  kind: "local",
  enabled: true,
  capabilities: { structuredOutput: true, tools: false, maxContext: 0 },
  isDefault: true,
  hasKey: false,
  ...o,
});
const res = (
  mid: string,
  labels: Partial<api.SuggestLabel>[] = [],
  o: Partial<api.SuggestResult> = {},
): api.SuggestResult => ({
  jobId: "job1",
  mid,
  ok: true,
  labels: labels.map((l, i) => ({
    name: `Label${i}`,
    confidence: 0.9,
    reason: `because ${i}`,
    existing: true,
    ...l,
  })),
  summary: `About ${mid}`,
  error: "",
  code: "",
  ...o,
});
const applyOk = (o: Partial<api.SuggestApply> = {}): api.SuggestApply => ({
  applied: ["mid5"],
  failed: null,
  appliedCount: 1,
  failedCount: 0,
  batchIds: ["b"],
  auditIds: [7, 8],
  createdLabels: [],
  ...o,
});

const folders = [["INBOX", "Inbox", "inbox"]].map(
  ([name, displayName, kind]) => ({
    name,
    displayName,
    kind: kind as api.MailboxKind,
    uidValidity: 7,
    messages: 1,
    bodies: 0,
    unseen: 0,
    lastSync: null,
  }),
);
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

/** Open the popup on the given list and send results for the first emails. */
async function openWith(
  results: api.SuggestResult[],
  p = provider(),
  list = msgs(5),
) {
  m.listProviders.mockResolvedValue([p]);
  m.suggest.mockResolvedValue("job1");
  m.suggestStatus.mockResolvedValue({
    jobId: "job1",
    provider: p.name,
    state: "running",
    total: 1,
    completed: 0,
    failed: 0,
    results: null,
  });
  await mount(list);
  await key("S");
  await screen.findByRole("dialog", { name: "Suggest labels" });
  await waitFor(() => expect(suggest.jobId).toBe("job1"));
  for (const r of results) emit("suggest:result", r);
}

beforeEach(() => {
  vi.clearAllMocks();
  handlers.clear();
  suggest.close();
});
afterEach(() => {
  suggest.close();
  cleanup();
});

describe("trigger", () => {
  it("S targets the cursor row and starts a job with the default provider", async () => {
    await openWith([]);
    expect(m.suggest).toHaveBeenCalledWith(["mid5"], "");
  });

  it("targets the selection when there is one", async () => {
    m.listProviders.mockResolvedValue([provider()]);
    m.suggest.mockResolvedValue("job1");
    m.suggestStatus.mockResolvedValue({} as api.SuggestJob);
    await mount();
    sel.toggleKey(keyOf(mail.shown[1]));
    sel.toggleKey(keyOf(mail.shown[2]));
    await key("S");
    await waitFor(() => expect(m.suggest).toHaveBeenCalled());
    expect(m.suggest.mock.calls[0][0].sort()).toEqual(["mid3", "mid4"]);
  });

  it("targets the open message when the message pane has focus", async () => {
    m.listProviders.mockResolvedValue([provider()]);
    m.suggest.mockResolvedValue("job1");
    m.suggestStatus.mockResolvedValue({} as api.SuggestJob);
    await mount();
    mail.openKey = keyOf(mail.shown[2]);
    layout.focus = "view";
    await key("S");
    await waitFor(() => expect(m.suggest).toHaveBeenCalled());
    expect(m.suggest.mock.calls[0][0]).toEqual(["mid3"]);
  });

  it("refuses more than 25 emails with a friendly message", async () => {
    await mount(msgs(30));
    for (const h of mail.shown) sel.toggleKey(keyOf(h));
    await key("S");
    await screen.findByText(/up to 25 emails at a time/);
    expect(m.suggest).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("has a toolbar button and a row button with the key in the tooltip", async () => {
    await mount();
    const tb = screen.getAllByRole("button", { name: "Suggest labels" });
    expect(tb.length).toBeGreaterThan(1);
    expect(tb.every((b) => b.title === "Suggest labels (S)")).toBe(true);
  });

  it("the row hover button suggests for that row only", async () => {
    m.listProviders.mockResolvedValue([provider()]);
    m.suggest.mockResolvedValue("job1");
    m.suggestStatus.mockResolvedValue({} as api.SuggestJob);
    await mount();
    const rowBtns = screen
      .getAllByRole("button", { name: "Suggest labels" })
      .filter((b) => b.closest(".hover"));
    await fireEvent.click(rowBtns[2]);
    await waitFor(() => expect(m.suggest).toHaveBeenCalledWith(["mid3"], ""));
  });

  it("the context menu has Suggest labels", async () => {
    m.listProviders.mockResolvedValue([provider()]);
    m.suggest.mockResolvedValue("job1");
    m.suggestStatus.mockResolvedValue({} as api.SuggestJob);
    await mount();
    await fireEvent.contextMenu(screen.getAllByRole("option")[0]);
    await fireEvent.click(
      await screen.findByRole("menuitem", { name: /Suggest labels/ }),
    );
    await waitFor(() => expect(m.suggest).toHaveBeenCalledWith(["mid5"], ""));
  });
});

describe("popup keys", () => {
  const two = [
    res("mid5", [{ name: "Work" }, { name: "Bills", confidence: 0.4 }]),
    res("mid4", [{ name: "Travel" }]),
  ];
  async function openTwo() {
    m.listProviders.mockResolvedValue([provider()]);
    m.suggest.mockResolvedValue("job1");
    m.suggestStatus.mockResolvedValue({} as api.SuggestJob);
    await mount();
    sel.toggleKey(keyOf(mail.shown[0]));
    sel.toggleKey(keyOf(mail.shown[1]));
    await key("S");
    await waitFor(() => expect(suggest.jobId).toBe("job1"));
    two.forEach((r) => emit("suggest:result", r));
    await screen.findByText("Work");
  }
  const opt = (name: string) =>
    screen.getByRole("option", { name: new RegExp(name) });

  it("pre-ticks confident existing labels only", async () => {
    await openTwo();
    expect(opt("Work").getAttribute("aria-selected")).toBe("true");
    expect(opt("Bills").getAttribute("aria-selected")).toBe("false");
  });

  it("j/k move, space and enter toggle", async () => {
    await openTwo();
    await key("j");
    expect(
      screen
        .getByRole("listbox", { name: /Suggested labels/ })
        .getAttribute("aria-activedescendant"),
    ).toBe("sg-l1");
    await key(" ");
    expect(opt("Bills").getAttribute("aria-selected")).toBe("true");
    await key("Enter");
    expect(opt("Bills").getAttribute("aria-selected")).toBe("false");
    await key("k");
    await key(" ");
    expect(opt("Work").getAttribute("aria-selected")).toBe("false");
  });

  it("l/h move between emails and the page indicator follows", async () => {
    await openTwo();
    expect(screen.getByText("1 of 2")).toBeTruthy();
    await key("l");
    await screen.findByText("Travel");
    expect(screen.getByText("2 of 2")).toBeTruthy();
    expect(screen.queryByText("Work")).toBeNull();
    await key("l"); // stops at the end
    expect(screen.getByText("2 of 2")).toBeTruthy();
    await key("h");
    await screen.findByText("Work");
    expect(screen.getByText("1 of 2")).toBeTruthy();
  });

  it("keys don't leak to the mailbox behind", async () => {
    await openTwo();
    await key("d");
    await key("e");
    expect(m.applyAction).not.toHaveBeenCalled();
  });

  it("per-email accept all / reject all", async () => {
    await openTwo();
    await fireEvent.click(screen.getByRole("button", { name: "Reject all" }));
    expect(opt("Work").getAttribute("aria-selected")).toBe("false");
    await fireEvent.click(screen.getByRole("button", { name: "Accept all" }));
    expect(opt("Bills").getAttribute("aria-selected")).toBe("true");
  });

  it("clicking a row toggles it", async () => {
    await openTwo();
    await fireEvent.click(opt("Bills"));
    expect(opt("Bills").getAttribute("aria-selected")).toBe("true");
  });

  it("Esc cancels and discards, without calling apply", async () => {
    await openTwo();
    await key("Escape");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(m.applySuggestions).not.toHaveBeenCalled();
    expect(m.suggestCancel).toHaveBeenCalledWith("job1"); // still running
  });

  it("? still opens the shortcuts overlay on top", async () => {
    await openTwo();
    await key("?");
    await screen.findByRole("dialog", { name: "Keyboard shortcuts" });
    expect(screen.getByText("Accept ticked labels")).toBeTruthy();
    await key("Escape"); // closes help only
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Keyboard shortcuts" }),
      ).toBeNull(),
    );
    expect(screen.getByRole("dialog", { name: "Suggest labels" })).toBeTruthy();
  });

  it("traps focus and restores it on close", async () => {
    await openTwo();
    const dlg = screen.getByRole("dialog", { name: "Suggest labels" });
    expect(dlg.contains(document.activeElement)).toBe(true);
    await key("Escape");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.activeElement?.closest("dialog, [role=dialog]")).toBeNull();
  });
});

describe("progressive results", () => {
  it("shows progress as results stream in and final counts on done", async () => {
    m.listProviders.mockResolvedValue([provider()]);
    m.suggest.mockResolvedValue("job1");
    m.suggestStatus.mockResolvedValue({} as api.SuggestJob);
    await mount();
    sel.toggleKey(keyOf(mail.shown[0]));
    sel.toggleKey(keyOf(mail.shown[1]));
    await key("S");
    await waitFor(() => expect(suggest.jobId).toBe("job1"));
    expect(screen.getAllByText("0 of 2 analysed").length).toBeGreaterThan(0);
    expect(screen.getByText("Analysing…")).toBeTruthy();
    emit("suggest:result", res("mid5", [{ name: "Work" }]));
    await screen.findByText("Work");
    expect(screen.getAllByText("1 of 2 analysed").length).toBeGreaterThan(0);
    emit("suggest:result", res("mid4", [{ name: "Travel" }]));
    emit("suggest:done", { jobId: "job1", total: 2, failed: 0 });
    await waitFor(() =>
      expect(screen.getAllByText("2 of 2 analysed").length).toBeGreaterThan(0),
    );
    expect(suggest.phase).toBe("done");
  });

  it("shows a per-email error with its code, and counts failures", async () => {
    await openWith([
      res("mid5", [], { ok: false, code: "too_large", error: "x" }),
    ]);
    emit("suggest:done", { jobId: "job1", total: 1, failed: 1 });
    await screen.findByText(/too long to analyse/);
    expect(screen.getByRole("alert").textContent).toContain("too_large");
    await waitFor(() =>
      expect(screen.getAllByText(/1 failed/).length).toBeGreaterThan(0),
    );
    expect(
      (screen.getByTitle(/Apply the ticked/) as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("ignores events of another job and keeps results that beat the job id", async () => {
    m.listProviders.mockResolvedValue([provider()]);
    let resolve!: (id: string) => void;
    m.suggest.mockReturnValue(new Promise((r) => (resolve = r)));
    m.suggestStatus.mockResolvedValue({
      jobId: "job1",
      provider: "ollama",
      state: "running",
      total: 1,
      completed: 0,
      failed: 0,
      results: null,
    });
    await mount();
    await key("S");
    await waitFor(() => expect(m.suggest).toHaveBeenCalled());
    emit("suggest:result", res("mid5", [{ name: "Early" }])); // before the id
    emit("suggest:result", {
      ...res("mid5", [{ name: "Other" }]),
      jobId: "zzz",
    });
    resolve("job1");
    await screen.findByText("Early");
    expect(screen.queryByText("Other")).toBeNull();
  });

  it("late join: picks up results from Status", async () => {
    m.listProviders.mockResolvedValue([provider()]);
    m.suggest.mockResolvedValue("job1");
    m.suggestStatus.mockResolvedValue({
      jobId: "job1",
      provider: "ollama",
      state: "done",
      total: 1,
      completed: 1,
      failed: 0,
      results: [res("mid5", [{ name: "FromStatus" }])],
    });
    await mount();
    await key("S");
    await screen.findByText("FromStatus");
    expect(suggest.phase).toBe("done");
  });

  it("a turned-off cloud provider shows a clear stop, nothing is sent", async () => {
    m.listProviders.mockResolvedValue([
      provider({ name: "or", kind: "cloud", enabled: false }),
    ]);
    m.suggest.mockRejectedValue(
      new Error("cloud_disabled: this cloud provider is turned off"),
    );
    await mount();
    await key("S");
    await screen.findByText(/cloud provider is turned off/);
    expect(screen.getByRole("button", { name: "Open Settings" })).toBeTruthy();
  });

  it("no provider configured explains what to do", async () => {
    m.listProviders.mockResolvedValue([]);
    await mount();
    await key("S");
    await screen.findByText(/No AI provider is set up yet/);
    expect(m.suggest).not.toHaveBeenCalled();
  });
});

describe("cancel", () => {
  it("Cancel while running calls Cancel(jobId) and discards", async () => {
    await openWith([]);
    await fireEvent.click(screen.getByTitle("Cancel and discard (Esc)"));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(m.suggestCancel).toHaveBeenCalledWith("job1");
    expect(m.applySuggestions).not.toHaveBeenCalled();
  });

  it("after done, closing doesn't call Cancel", async () => {
    await openWith([res("mid5", [{ name: "Work" }])]);
    emit("suggest:done", { jobId: "job1", total: 1, failed: 0 });
    await waitFor(() => expect(suggest.phase).toBe("done"));
    await key("Escape");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(m.suggestCancel).not.toHaveBeenCalled();
  });
});

describe("cloud privacy", () => {
  it("shows the provider, model and the privacy note for cloud", async () => {
    await openWith(
      [],
      provider({ name: "openrouter", model: "gpt-x", kind: "cloud" }),
    );
    const dlg = screen.getByRole("dialog", { name: "Suggest labels" });
    expect(within(dlg).getByText(/openrouter · gpt-x/)).toBeTruthy();
    expect(dlg.textContent).toContain(
      "This email's content is sent to openrouter",
    );
    expect(within(dlg).getByText("Cloud")).toBeTruthy();
  });

  it("local providers say nothing leaves the computer", async () => {
    await openWith([]);
    const dlg = screen.getByRole("dialog", { name: "Suggest labels" });
    expect(dlg.textContent).not.toContain("is sent to");
    expect(dlg.textContent).toContain("Nothing leaves it");
  });
});

describe("accept", () => {
  it("sends only ticked labels per email, once, and closes with an undo toast", async () => {
    m.applySuggestions.mockResolvedValue(
      applyOk({ applied: ["mid5", "mid4"], auditIds: [7, 8] }),
    );
    m.listProviders.mockResolvedValue([provider()]);
    m.suggest.mockResolvedValue("job1");
    m.suggestStatus.mockResolvedValue({} as api.SuggestJob);
    await mount();
    sel.toggleKey(keyOf(mail.shown[0]));
    sel.toggleKey(keyOf(mail.shown[1]));
    await key("S");
    await waitFor(() => expect(suggest.jobId).toBe("job1"));
    emit(
      "suggest:result",
      res("mid5", [{ name: "Work" }, { name: "Bills", confidence: 0.3 }]),
    );
    emit(
      "suggest:result",
      res("mid4", [{ name: "Brand new", existing: false }]),
    );
    await screen.findByText("Work");
    await key("l");
    expect(await screen.findByText("new")).toBeTruthy();
    await key("Enter"); // tick the new label (not pre-ticked)
    await key("a");
    await waitFor(() => expect(m.applySuggestions).toHaveBeenCalledTimes(1));
    expect(m.applySuggestions).toHaveBeenCalledWith("job1", {
      mid5: ["Work"],
      mid4: ["Brand new"],
    });
    await screen.findByText("Applied 2 labels to 2 messages");
    expect(
      screen.getByRole("button", { name: "Undo this action" }),
    ).toBeTruthy();
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Suggest labels" }),
      ).toBeNull(),
    );
  });

  it("Undo reverts exactly the returned audit rows", async () => {
    m.applySuggestions.mockResolvedValue(applyOk());
    await openWith([res("mid5", [{ name: "Work" }])]);
    await fireEvent.click(screen.getByTitle(/Apply the ticked/));
    await fireEvent.click(
      await screen.findByRole("button", { name: "Undo this action" }),
    );
    await waitFor(() => expect(m.undoAction).toHaveBeenCalledTimes(2));
    expect(m.undoAction.mock.calls.map((c) => c[0])).toEqual([7, 8]);
  });

  it("disables Accept with nothing ticked and while applying", async () => {
    let done!: (v: api.SuggestApply) => void;
    m.applySuggestions.mockReturnValue(new Promise((r) => (done = r)));
    await openWith([res("mid5", [{ name: "Work" }])]);
    const accept = () =>
      screen.getByTitle(/Apply the ticked/) as HTMLButtonElement;
    await screen.findByText("Work");
    expect(accept().disabled).toBe(false);
    await key("a");
    await key("a"); // second press is ignored while applying
    await waitFor(() => expect(accept().disabled).toBe(true));
    expect(accept().textContent).toContain("Applying");
    expect(m.applySuggestions).toHaveBeenCalledTimes(1);
    done(applyOk());
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Suggest labels" }),
      ).toBeNull(),
    );
  });

  it("with nothing ticked Accept is disabled", async () => {
    await openWith([res("mid5", [{ name: "Work", confidence: 0.2 }])]);
    await screen.findByText("Work");
    expect(
      (screen.getByTitle(/Apply the ticked/) as HTMLButtonElement).disabled,
    ).toBe(true);
    await key("a");
    expect(m.applySuggestions).not.toHaveBeenCalled();
  });

  it("keeps the popup open on failures and lists them with their codes", async () => {
    m.applySuggestions.mockResolvedValue(
      applyOk({
        applied: [],
        failed: [
          { mid: "mid5", mailbox: "", message: "x", code: "create_failed" },
        ],
        appliedCount: 0,
        failedCount: 1,
        auditIds: [],
      }),
    );
    await openWith([res("mid5", [{ name: "Work" }])]);
    await key("a");
    const al = await screen.findByText(/Couldn't create the new label/);
    expect(al).toBeTruthy();
    expect(screen.getByRole("dialog", { name: "Suggest labels" })).toBeTruthy();
    expect(suggest.applying).toBe(false);
  });

  it("shows a thrown apply error and stays open", async () => {
    m.applySuggestions.mockRejectedValue(new Error("boom"));
    await openWith([res("mid5", [{ name: "Work" }])]);
    await key("a");
    await screen.findByText("boom");
    expect(screen.getByRole("dialog", { name: "Suggest labels" })).toBeTruthy();
  });
});
