import * as api from "../api";
import { keyOf, isUnread } from "../mail/format";
import {
  MAX_BATCH,
  chunk,
  doneText,
  failureText,
  undoCodeText,
  mergeResults,
} from "../mail/actions";
import { layout } from "./layout.svelte";
import { mail, errText } from "./mail.svelte";
import { sel } from "./select.svelte";
import { ui } from "./ui.svelte";

export const TOAST_MS = 8000;
const ADVANCE: api.ActionType[] = [
  "archive",
  "trash",
  "spam",
  "not_spam",
  "move",
];

export interface Toast {
  id: number;
  kind: "ok" | "error" | "info";
  text: string;
  /** Show an Undo button. */
  undo: boolean;
  /** The exact audit rows this toast's Undo reverts. */
  auditIds?: number[];
}

export const isStarred = (h: api.MessageHeader): boolean =>
  (h.flags ?? []).some((f) => f.toLowerCase() === "\\flagged");

/** A toast offers Undo only for rows Go says are undoable. */
const undoOf = (res: api.ActionResult) => {
  const auditIds = res.auditIds ?? [];
  return { undo: auditIds.length > 0, auditIds };
};

class Actions {
  /** Rows an action is running on (message keys). */
  busy = $state<Record<string, true>>({});
  progress = $state("");
  toast = $state<Toast | null>(null);
  activity = $state<api.AuditEntry[]>([]);
  activityError = $state("");
  activityLoading = $state(false);
  /** The messages a picker (move, labels) was opened for. */
  pending = $state<api.MessageHeader[]>([]);
  private seq = 0;
  private running = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private doneTimer: ReturnType<typeof setTimeout> | undefined;

  get isBusy(): boolean {
    return this.running > 0;
  }

  /** The messages an action applies to: selection, else open, else cursor. */
  targets(): api.MessageHeader[] {
    if (sel.count > 0) {
      const set = sel.set;
      return mail.shown.filter((m) => set.has(keyOf(m)));
    }
    if (layout.focus === "view" && mail.openKey) {
      const h = mail.messages.find((m) => keyOf(m) === mail.openKey);
      if (h) return [h];
      if (mail.message && !mail.openGone) return [mail.message];
      return [];
    }
    if (layout.focus === "list") {
      const h = mail.shown[mail.cursorIndex];
      return h ? [h] : [];
    }
    return [];
  }

  /** Open a picker for the current targets, or say there is nothing to act on. */
  openPicker(kind: "move" | "label"): void {
    const t = this.targets();
    if (t.length === 0) return ui.flash("No message selected");
    this.pending = t;
    ui.overlay = kind;
  }

  // ---- toasts ----

  showToast(t: Omit<Toast, "id">, ms = TOAST_MS): void {
    clearTimeout(this.timer);
    this.toast = { ...t, id: ++this.seq };
    this.armToast(ms);
  }
  private armToast(ms: number): void {
    const id = this.toast?.id;
    this.timer = setTimeout(() => {
      if (this.toast?.id === id) this.toast = null;
    }, ms);
  }
  holdToast(): void {
    clearTimeout(this.timer);
  }
  releaseToast(): void {
    if (this.toast) this.armToast(TOAST_MS);
  }
  dismissToast(): void {
    clearTimeout(this.timer);
    this.toast = null;
  }

  // ---- apply ----

  /** The row to land on after `keys` leave the list: the next, else previous. */
  private nextKey(keys: string[]): string | null {
    const gone = new Set(keys);
    const list = mail.shown;
    const from = Math.max(0, mail.cursorIndex);
    for (let i = from; i < list.length; i++)
      if (!gone.has(keyOf(list[i]))) return keyOf(list[i]);
    for (let i = from - 1; i >= 0; i--)
      if (!gone.has(keyOf(list[i]))) return keyOf(list[i]);
    return null;
  }

  /**
   * Apply one action to the targets, in chunks of 100. Rows show a spinner
   * until Go answers; then the list is re-read from the store.
   */
  async run(
    type: api.ActionType,
    params: { folder?: string; label?: string } = {},
    opts: { label?: string; headers?: api.MessageHeader[] } = {},
  ): Promise<api.ActionResult | null> {
    const targets = (opts.headers ?? this.targets()).filter(
      (h) => !this.busy[keyOf(h)],
    );
    if (targets.length === 0) {
      ui.flash(
        this.isBusy ? "Still working on those messages" : "No message selected",
      );
      return null;
    }
    const withId = targets.filter((h) => h.internalId);
    if (withId.length === 0) {
      ui.flash("These messages aren't ready yet. Try again after sync.");
      return null;
    }
    const keys = withId.map(keyOf);
    const ids = withId.map((h) => h.internalId as string);
    const advance = ADVANCE.includes(type);
    const next = advance ? this.nextKey(keys) : null;
    const viewing = layout.focus === "view";

    this.running++;
    this.busy = {
      ...this.busy,
      ...Object.fromEntries(keys.map((k) => [k, true as const])),
    };
    const parts = chunk(ids, MAX_BATCH);
    const results: api.ActionResult[] = [];
    try {
      for (let i = 0; i < parts.length; i++) {
        if (parts.length > 1) this.progress = `${i + 1} of ${parts.length}`;
        try {
          results.push(
            await api.applyAction({ type, params, messageIds: parts[i] }),
          );
        } catch (e) {
          const message = errText(e);
          results.push({
            applied: [],
            failed: parts[i].map((mid) => ({ mid, mailbox: "", message })),
            appliedCount: 0,
            failedCount: parts[i].length,
            auditIds: [],
          });
        }
      }
    } finally {
      this.progress = "";
    }
    const res = mergeResults(results);
    try {
      await mail.refreshHead();
    } finally {
      const b = { ...this.busy };
      for (const k of keys) delete b[k];
      this.busy = b;
      this.running--;
    }
    if (res.appliedCount > 0) {
      sel.clear();
      if (next && mail.shown.some((m) => keyOf(m) === next)) {
        mail.cursorKey = next;
        if (viewing) {
          const h = mail.shown.find((m) => keyOf(m) === next);
          if (h) void mail.open(h);
        }
      }
    }
    this.report(
      type,
      ids.length,
      res,
      opts.label ?? params.folder ?? params.label,
    );
    return res;
  }

  private report(
    type: api.ActionType,
    total: number,
    res: api.ActionResult,
    param?: string,
  ): void {
    const failed = res.failed ?? [];
    if (failed.length > 0) {
      const f = failureText(failed, total);
      const ok =
        res.appliedCount > 0
          ? `${doneText(type, res.appliedCount, param)}. `
          : "";
      this.showToast(
        { kind: "error", text: ok + f, ...undoOf(res) },
        TOAST_MS * 2,
      );
    } else if (res.appliedCount > 0) {
      this.showToast({
        kind: "ok",
        text: doneText(type, res.appliedCount, param),
        ...undoOf(res),
      });
    }
  }

  // ---- undo ----

  private async finishUndo(
    p: () => Promise<api.ActionResult>,
  ): Promise<void> {
    this.running++;
    try {
      const res = await p();
      const failed = res.failed ?? [];
      if (res.code && res.appliedCount === 0 && failed.length === 0)
        this.showToast(
          { kind: "info", text: undoCodeText(res.code), undo: false },
          TOAST_MS,
        );
      else if (failed.length > 0)
        this.showToast(
          {
            kind: "error",
            text: failureText(failed, failed.length + res.appliedCount),
            undo: false,
          },
          TOAST_MS * 2,
        );
      else
        this.showToast({
          kind: "info",
          text: `Undone · ${res.appliedCount} ${res.appliedCount === 1 ? "message" : "messages"}`,
          undo: false,
        });
    } catch (e) {
      this.showToast(
        { kind: "error", text: errText(e), undo: false },
        TOAST_MS * 2,
      );
    } finally {
      this.running--;
    }
    await mail.refreshHead();
    if (ui.overlay === "activity") void this.loadActivity();
  }

  /** Undo the newest action (the z key, toolbar and palette). */
  undoLast(): Promise<void> {
    return this.finishUndo(() => api.undoLast());
  }

  /** Undo exactly these audit rows (one batch), one Undo call each. */
  undoBatch(auditIds: number[]): Promise<void> {
    return this.finishUndo(async () => {
      const rs: api.ActionResult[] = [];
      for (const id of auditIds) rs.push(await api.undoAction(id));
      // A refusal on only some rows still counts as undone for the rest.
      const merged = mergeResults(rs);
      if (merged.appliedCount > 0 || (merged.failed ?? []).length > 0)
        merged.code = "";
      return merged;
    });
  }

  // ---- activity ----

  async loadActivity(): Promise<void> {
    this.activityLoading = true;
    try {
      this.activity = (await api.recentActions(50)) ?? [];
      this.activityError = "";
    } catch (e) {
      this.activityError = errText(e);
    } finally {
      this.activityLoading = false;
    }
  }

  // ---- actions:done ----

  /** The Go side finished something (maybe from elsewhere): refresh, once. */
  noteDone(delay = 250): void {
    if (this.doneTimer !== undefined) return;
    this.doneTimer = setTimeout(() => {
      this.doneTimer = undefined;
      if (this.running > 0) return; // our own call refreshes when it ends
      void mail.refreshHead();
      if (ui.overlay === "activity") void this.loadActivity();
    }, delay);
  }
  stop(): void {
    clearTimeout(this.doneTimer);
    this.doneTimer = undefined;
    clearTimeout(this.timer);
  }

  // ---- toggles chosen from the current targets ----

  toggleRead(): Promise<api.ActionResult | null> {
    const t = this.targets();
    return this.run(
      t.length > 0 && t.every((h) => !isUnread(h))
        ? "mark_unread"
        : "mark_read",
      {},
      { headers: t },
    );
  }
  toggleStar(): Promise<api.ActionResult | null> {
    const t = this.targets();
    return this.run(
      t.length > 0 && t.every(isStarred) ? "unstar" : "star",
      {},
      { headers: t },
    );
  }
}

export const actions = new Actions();
