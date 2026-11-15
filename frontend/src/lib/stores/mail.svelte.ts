import * as api from "../api";
import { orderFolders } from "../mail/folders";
import { keyOf, matchesFilter, mergeHead } from "../mail/format";

export const PAGE = 200;
const NEAR_END = 40;

export class Mail {
  folders = $state<api.MailboxDTO[]>([]);
  foldersLoaded = $state(false);
  foldersError = $state("");

  current = $state<string | null>(null);
  messages = $state<api.MessageHeader[]>([]);
  next = $state<api.PageCursor | null>(null);
  loading = $state(false);
  error = $state("");
  filter = $state("");

  /** The list cursor, by message key (never by index). */
  cursorKey = $state<string | null>(null);
  /** The folder cursor in the folder pane. */
  folderCursor = $state<string | null>(null);

  openKey = $state<string | null>(null);
  message = $state<api.Message | null>(null);
  messageLoading = $state(false);
  messageError = $state("");
  plain = $state(false);

  private req = 0;
  private msgReq = 0;

  shown = $derived(
    this.filter.trim()
      ? this.messages.filter((m) => matchesFilter(m, this.filter))
      : this.messages,
  );
  cursorIndex = $derived(
    this.cursorKey === null
      ? -1
      : this.shown.findIndex((m) => keyOf(m) === this.cursorKey),
  );
  folder = $derived(this.folders.find((f) => f.name === this.current) ?? null);

  async loadFolders(): Promise<void> {
    try {
      this.folders = orderFolders(await api.listMailboxes());
      this.foldersError = "";
    } catch (e) {
      this.foldersError = errText(e);
    } finally {
      this.foldersLoaded = true;
    }
    if (this.current === null && this.folders.length > 0) {
      await this.selectFolder(
        (this.folders.find((f) => f.kind === "inbox") ?? this.folders[0]).name,
      );
    }
  }

  async selectFolder(name: string): Promise<void> {
    if (name === this.current && this.messages.length > 0) return;
    this.current = name;
    this.folderCursor = name;
    this.messages = [];
    this.next = null;
    this.cursorKey = null;
    this.openKey = null;
    this.message = null;
    this.messageError = "";
    this.filter = "";
    await this.loadPage(true);
    if (this.cursorKey === null && this.shown.length > 0)
      this.cursorKey = keyOf(this.shown[0]);
  }

  /** Reload the current folder from scratch (stale epoch, retry). */
  async reload(): Promise<void> {
    const name = this.current;
    this.current = null;
    if (name) await this.selectFolder(name);
  }

  async loadPage(first: boolean): Promise<void> {
    const name = this.current;
    if (!name || this.loading) return;
    if (!first && !this.next) return;
    const id = ++this.req;
    this.loading = true;
    this.error = "";
    try {
      const page = await api.listMessages(name, first ? null : this.next, PAGE);
      if (id !== this.req || name !== this.current) return;
      const got = page.messages ?? [];
      this.messages = first ? got : [...this.messages, ...got];
      this.next = page.next;
    } catch (e) {
      if (id === this.req) this.error = errText(e);
    } finally {
      if (id === this.req) this.loading = false;
    }
  }

  /** Pick up new mail at the top without moving the cursor or the open message. */
  async refreshHead(): Promise<void> {
    const name = this.current;
    try {
      const boxes = await api.listMailboxes();
      this.folders = orderFolders(boxes);
      this.foldersError = "";
      this.foldersLoaded = true;
    } catch {
      /* keep what we have */
    }
    if (!name) {
      if (this.folders.length > 0) await this.loadFolders();
      return;
    }
    if (this.loading) return;
    try {
      const page = await api.listMessages(name, null, PAGE);
      if (name !== this.current) return;
      const fresh = page.messages ?? [];
      if (this.messages.length === 0) {
        this.messages = fresh;
        this.next = page.next;
        if (this.cursorKey === null && fresh.length > 0)
          this.cursorKey = keyOf(fresh[0]);
      } else {
        this.messages = mergeHead(this.messages, fresh);
      }
    } catch {
      /* a failed background refresh is not worth an error banner */
    }
  }

  // ---- cursor ----

  moveCursor(delta: number): void {
    const n = this.shown.length;
    if (n === 0) return;
    const from = this.cursorIndex < 0 ? (delta > 0 ? -1 : n) : this.cursorIndex;
    this.cursorTo(from + delta);
  }

  cursorTo(index: number): void {
    const n = this.shown.length;
    if (n === 0) return;
    const i = Math.min(n - 1, Math.max(0, index));
    this.cursorKey = keyOf(this.shown[i]);
    if (n - i < NEAR_END) void this.loadPage(false);
  }

  /** Last loaded row, or the true end once everything is loaded. */
  async cursorToEnd(): Promise<void> {
    this.cursorTo(this.shown.length - 1);
    // Keep paging so G reaches the real last message, with a safety bound.
    for (let i = 0; i < 500 && this.next && !this.filter; i++) {
      await this.loadPage(false);
      this.cursorTo(this.shown.length - 1);
    }
  }

  setFilter(q: string): void {
    this.filter = q;
    const first = this.shown[0];
    if (this.cursorIndex < 0) this.cursorKey = first ? keyOf(first) : null;
  }

  // ---- folder cursor ----

  moveFolder(delta: number): void {
    const names = this.folders.map((f) => f.name);
    if (names.length === 0) return;
    const i = names.indexOf(this.folderCursor ?? this.current ?? "");
    const next = Math.min(
      names.length - 1,
      Math.max(0, (i < 0 ? 0 : i) + delta),
    );
    this.folderCursor = names[next];
  }

  // ---- message ----

  async open(h: api.MessageHeader): Promise<void> {
    const key = keyOf(h);
    this.cursorKey = key;
    if (key === this.openKey && this.message) return;
    this.openKey = key;
    this.message = null;
    this.messageError = "";
    this.messageLoading = true;
    this.plain = false;
    const id = ++this.msgReq;
    try {
      const m = await api.getMessage(h.mailbox, h.uidValidity, h.uid);
      if (id !== this.msgReq) return;
      this.message = m;
    } catch (e) {
      if (id !== this.msgReq) return;
      const t = errText(e);
      if (/reset on the server|stale/i.test(t)) {
        await this.reload();
        this.messageError =
          "This folder was reset on the server, so it was reloaded. Open the message again.";
      } else this.messageError = t;
    } finally {
      if (id === this.msgReq) this.messageLoading = false;
    }
  }

  async openCursor(): Promise<void> {
    const h = this.shown[this.cursorIndex];
    if (h) await this.open(h);
  }

  /** Next or previous message in the list, opening it. */
  async step(dir: -1 | 1): Promise<void> {
    const i = this.cursorIndex + dir;
    if (i < 0 || i >= this.shown.length) return;
    this.cursorTo(i);
    await this.open(this.shown[i]);
  }
}

export const errText = (e: unknown): string => {
  const m = e instanceof Error ? e.message : String(e ?? "");
  return m || "Something went wrong.";
};

export const mail = new Mail();
