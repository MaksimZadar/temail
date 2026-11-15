import * as api from "../api";

export type FolderState = "waiting" | "syncing" | "done" | "failed";
export type Phase = "idle" | "connecting" | "headers" | "bodies";

export interface FolderRow {
  name: string;
  display: string;
  state: FolderState;
  headersDone: number;
  headersTotal: number;
  bodiesDone: number;
  bodiesTotal: number;
  /** Messages cached, as last known. */
  messages: number;
  error: string;
  /** 0..1 progress of this folder across both phases. */
  fraction: number;
}

interface Work {
  name: string;
  display: string;
  order: number;
  headersDone: number;
  headersTotal: number;
  headersFinished: boolean;
  bodiesDone: number;
  bodiesTotal: number;
  bodiesSeen: boolean;
  messages: number;
  failed: string;
  touched: boolean;
}

const FLUSH_MS = 250;
const STATE_RANK: Record<FolderState, number> = {
  syncing: 0,
  waiting: 1,
  done: 2,
  failed: 3,
};

function toRow(w: Work, phase: Phase, finished: boolean): FolderRow {
  const hFrac = w.headersFinished
    ? 1
    : w.headersTotal > 0
      ? Math.min(1, w.headersDone / w.headersTotal)
      : 0;
  const bFrac =
    w.bodiesTotal > 0
      ? Math.min(1, w.bodiesDone / w.bodiesTotal)
      : w.bodiesSeen
        ? 1
        : 0;
  let state: FolderState;
  if (w.failed) state = "failed";
  else if (finished) state = "done";
  else if (
    phase === "bodies" &&
    w.headersFinished &&
    w.bodiesSeen &&
    bFrac >= 1
  )
    state = "done";
  else if (w.touched) state = "syncing";
  else state = "waiting";
  const fraction = state === "done" ? 1 : (hFrac + bFrac) / 2;
  return {
    name: w.name,
    display: w.display,
    state,
    headersDone: w.headersDone,
    headersTotal: w.headersTotal,
    bodiesDone: w.bodiesDone,
    bodiesTotal: w.bodiesTotal,
    messages: Math.max(w.messages, w.bodiesTotal),
    error: w.failed,
    fraction,
  };
}

/** Active first, then waiting, done, failed. Stable inside each group. */
export function sortRows(rows: FolderRow[]): FolderRow[] {
  return rows
    .map((r, i) => [r, i] as const)
    .sort(
      (a, b) => STATE_RANK[a[0].state] - STATE_RANK[b[0].state] || a[1] - b[1],
    )
    .map(([r]) => r);
}

export function relativeTime(from: number, now: number): string {
  const s = Math.max(0, Math.round((now - from) / 1000));
  if (s < 45) return "just now";
  const m = Math.round(s / 60);
  if (m < 60) return m <= 1 ? "1 min ago" : `${m} min ago`;
  const h = Math.round(m / 60);
  return h === 1 ? "1 hour ago" : `${h} hours ago`;
}

class Sync {
  // Published (reactive) state. Updated at most every FLUSH_MS.
  /** The initial sync is in progress. Not the same as "live": see `watching`. */
  running = $state(false);
  /** The change watcher is up (after the initial sync). */
  watching = $state(false);
  /** The live (IDLE) connection is up. False while the watcher polls. */
  idle = $state(false);
  lastChecked = $state<number | null>(null);
  phase = $state<Phase>("idle");
  rows = $state<FolderRow[]>([]);
  error = $state("");
  finishedAt = $state<number | null>(null);
  hasResult = $state(false);
  now = $state(Date.now());
  /** Short text for screen readers. Changes only when a run starts or ends. */
  announcement = $state("");

  // Working state, written by every event, copied out on a timer.
  private work = new Map<string, Work>();
  private nextOrder = 0;
  private finished = false;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private tick: ReturnType<typeof setInterval> | undefined;
  private seeded = false;
  private offs: (() => void)[] = [];

  get total(): number {
    return this.rows.length;
  }
  get doneCount(): number {
    return this.rows.filter((r) => r.state === "done").length;
  }
  get failed(): FolderRow[] {
    return this.rows.filter((r) => r.state === "failed");
  }
  get sorted(): FolderRow[] {
    return sortRows(this.rows);
  }
  /** Messages cached across all folders. */
  get messages(): number {
    return this.rows.reduce((n, r) => n + r.messages, 0);
  }
  /** Overall progress 0..1, never decreasing within a run. */
  get fraction(): number | null {
    if (!this.running || this.rows.length === 0) return null;
    return this.rows.reduce((n, r) => n + r.fraction, 0) / this.rows.length;
  }
  /** One stable line for the footer. */
  get text(): string {
    if (this.error) return "Sync failed";
    if (this.running) {
      if (
        this.phase === "connecting" ||
        this.phase === "idle" ||
        this.total === 0
      )
        return "Syncing…";
      if (this.phase === "bodies")
        return `Downloading messages · ${this.doneCount} of ${this.total} folders`;
      const n = this.rows.filter(
        (r) => r.state !== "waiting" && this.isHeadersFinished(r),
      ).length;
      return `Syncing folders · ${n} of ${this.total}`;
    }
    if (this.watching) {
      if (this.idle) return "Live";
      return this.lastChecked !== null
        ? `Up to date · checked ${relativeTime(this.lastChecked, this.now)}`
        : "Up to date";
    }
    if (this.finishedAt !== null)
      return `Synced · ${relativeTime(this.finishedAt, this.now)}`;
    return this.hasResult ? "Synced" : "Waiting to sync";
  }

  /** The watcher works by polling because the live connection is down. */
  get polling(): boolean {
    return this.watching && !this.idle && !this.error;
  }
  /** Tooltip for the status line while watching. */
  get detail(): string {
    if (!this.watching) return "Show sync details";
    const when =
      this.lastChecked !== null
        ? ` Last checked ${relativeTime(this.lastChecked, this.now)}.`
        : "";
    return this.idle
      ? `Watching for new mail.${when}`
      : `The live connection is down, so temail checks every so often.${when}`;
  }

  private isHeadersFinished(r: FolderRow): boolean {
    return this.work.get(r.name)?.headersFinished ?? false;
  }

  private ensure(name: string, display?: string, messages = 0): Work {
    let w = this.work.get(name);
    if (!w) {
      w = {
        name,
        display: display || name,
        order: this.nextOrder++,
        headersDone: 0,
        headersTotal: 0,
        headersFinished: false,
        bodiesDone: 0,
        bodiesTotal: 0,
        bodiesSeen: false,
        messages,
        failed: "",
        touched: false,
      };
      this.work.set(name, w);
    } else {
      if (display) w.display = display;
      if (messages) w.messages = messages;
    }
    return w;
  }

  private async seed(): Promise<void> {
    if (this.seeded) return;
    this.seeded = true;
    try {
      const boxes = await api.listMailboxes();
      for (const b of boxes) this.ensure(b.name, b.displayName, b.messages);
      this.schedule();
    } catch {
      this.seeded = false;
    }
  }

  /** Handle one progress event. Cheap; publishing is coalesced. */
  onProgress(p: api.ProgressEvent): void {
    // While watching, resyncs after a change are not an "initial sync". They
    // must not flip the footer back to Syncing.
    if (this.watching) return;
    if (this.finished) {
      // A new run after a finished one.
      this.resetRun();
    }
    this.running = true;
    this.error = "";
    if (this.announcement !== "Sync started")
      this.announcement = "Sync started";

    if (p.state === "connecting" || p.state === "mailboxes") {
      this.phase = "connecting";
    } else if (p.state === "bodies") {
      this.phase = "bodies";
      for (const w of this.work.values())
        if (w.touched) w.headersFinished = true;
    }
    if (p.mailbox) {
      void this.seed();
      const w = this.ensure(p.mailbox);
      if (p.state === "headers" || p.state === "reconciling") {
        if (this.phase !== "bodies") this.phase = "headers";
        // Moving on to another folder means earlier folders' headers are in.
        for (const o of this.work.values())
          if (o !== w && o.touched && !o.headersFinished && o.order < w.order)
            o.headersFinished = true;
        w.touched = true;
        w.headersDone = p.headersDone;
        w.headersTotal = p.headersTotal;
      } else if (p.state === "bodies") {
        w.touched = true;
        w.headersFinished = true;
        w.bodiesSeen = true;
        w.bodiesDone = p.bodiesDone;
        w.bodiesTotal = p.bodiesTotal;
        w.messages = Math.max(w.messages, p.bodiesTotal);
      }
    }
    this.schedule();
  }

  onDone(r: api.SyncResult): void {
    this.finished = true;
    for (const e of r.mailboxErrors ?? []) {
      const w = this.ensure(e.mailbox);
      w.failed = e.message || "Couldn't sync this folder.";
    }
    this.running = false;
    this.phase = "idle";
    this.hasResult = true;
    this.finishedAt = Date.now();
    this.now = this.finishedAt;
    this.announcement = r.mailboxErrors?.length
      ? "Sync finished. Some folders couldn't sync."
      : "Sync finished";
    this.seeded = false;
    void this.seed().then(() => this.flush());
    this.flush();
    // The watcher starts right after the initial sync: pick up its state.
    void this.refreshStatus();
  }

  /** Read watching/idle/lastChecked. `running` is the initial sync only. */
  async refreshStatus(): Promise<void> {
    try {
      const s = await api.syncStatus();
      this.now = Date.now();
      this.watching = !!s.watching;
      this.idle = !!s.idle;
      const t = s.lastChecked ? Date.parse(s.lastChecked) : NaN;
      this.lastChecked = Number.isNaN(t) ? null : t;
      if (s.watching) {
        // The initial sync is over. Never leave the spinner on.
        this.running = false;
        this.phase = "idle";
        this.finished = true;
        this.hasResult = true;
        if (s.lastResult || this.rows.length > 0) this.flush();
      }
    } catch {
      /* the status is optional; the next event tries again */
    }
  }

  onError(message: string): void {
    this.running = false;
    this.watching = false;
    this.idle = false;
    this.phase = "idle";
    this.error = message || "Sync failed.";
    this.announcement = "Sync failed";
    this.flush();
  }

  private resetRun(): void {
    this.finished = false;
    this.finishedAt = null;
    this.error = "";
    for (const w of this.work.values()) {
      w.headersDone = w.headersTotal = 0;
      w.headersFinished = w.bodiesSeen = w.touched = false;
      w.bodiesDone = w.bodiesTotal = 0;
      w.failed = "";
    }
  }

  private schedule(): void {
    if (this.timer !== undefined) return;
    this.timer = setTimeout(() => this.flush(), FLUSH_MS);
  }

  /** Copy the working state to the reactive rows now. */
  flush(): void {
    clearTimeout(this.timer);
    this.timer = undefined;
    const rows = [...this.work.values()]
      .sort((a, b) => a.order - b.order)
      .map((w) => toRow(w, this.phase, this.finished && !this.running));
    // Keep the overall figure from stepping backwards between flushes.
    const prev = new Map(this.rows.map((r) => [r.name, r.fraction]));
    for (const r of rows) {
      const before = prev.get(r.name);
      if (this.running && before !== undefined && r.fraction < before)
        r.fraction = before;
    }
    this.rows = rows;
  }

  async start(): Promise<void> {
    this.stop();
    this.offs = [
      api.onEvent<api.ProgressEvent>("sync:progress", (p) =>
        this.onProgress(p),
      ),
      api.onEvent<api.SyncResult>("sync:done", (r) => this.onDone(r)),
      api.onEvent<{ message: string }>("sync:error", (e) =>
        this.onError(e.message),
      ),
      // A check found changes, so lastChecked and idle may have moved.
      api.onEvent<{ mailboxes: string[] }>(
        "sync:changed",
        () => void this.refreshStatus(),
      ),
    ];
    this.tick = setInterval(() => {
      this.now = Date.now();
      if (this.watching) void this.refreshStatus();
    }, 30_000);
    try {
      const s = await api.syncStatus();
      this.now = Date.now();
      this.running = !!s.running;
      this.watching = !!s.watching;
      this.idle = !!s.idle;
      const t = s.lastChecked ? Date.parse(s.lastChecked) : NaN;
      this.lastChecked = Number.isNaN(t) ? null : t;
      if (s.watching) {
        this.running = false;
        this.hasResult = true;
        this.finished = true;
      }
      if (s.lastResult && !this.hasResult) this.hasResult = true;
      if (s.lastError && !this.hasResult) this.error = s.lastError;
    } catch {
      /* status is optional; events still arrive */
    }
    await this.seed();
    this.flush();
  }

  stop(): void {
    this.offs.forEach((f) => f());
    this.offs = [];
    clearInterval(this.tick);
    clearTimeout(this.timer);
    this.timer = undefined;
  }

  /** Test helper: forget everything. */
  reset(): void {
    this.stop();
    this.work.clear();
    this.nextOrder = 0;
    this.finished = false;
    this.seeded = false;
    this.running = false;
    this.watching = false;
    this.idle = false;
    this.lastChecked = null;
    this.phase = "idle";
    this.rows = [];
    this.error = "";
    this.finishedAt = null;
    this.hasResult = false;
    this.announcement = "";
  }
}

export const sync = new Sync();
