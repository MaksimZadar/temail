import * as api from "../api";
import {
  MAX_SUGGEST,
  codeText,
  defaultTick,
  labelKey,
  plural,
} from "../suggest/text";
import { actions } from "./actions.svelte";
import { mail, errText } from "./mail.svelte";
import { ui } from "./ui.svelte";

export type Phase = "starting" | "running" | "done" | "cancelled" | "failed";

export interface Target {
  mid: string;
  header: api.MessageHeader;
}

export interface ApplyFailure {
  mid: string;
  text: string;
  /** Server text for this email, if any (can be long). */
  detail: string;
}

/** Pick the provider the way Go does, for display only. */
export function pickProvider(
  list: api.LLMProvider[],
  name = "",
): api.LLMProvider | null {
  if (name) return list.find((p) => p.name === name) ?? null;
  return (
    list.find((p) => p.isDefault) ??
    list.find((p) => p.kind === "local" && p.enabled) ??
    list.find((p) => p.enabled) ??
    null
  );
}

class Suggest {
  open = $state(false);
  phase = $state<Phase>("starting");
  /** A problem that stops the whole job (no provider, cloud off, …). */
  fatal = $state<{ code: string; text: string } | null>(null);
  jobId = $state("");
  targets = $state<Target[]>([]);
  results = $state<Record<string, api.SuggestResult>>({});
  /** mid -> lower-case label name -> ticked */
  ticks = $state<Record<string, Record<string, boolean>>>({});
  applied = $state<Record<string, true>>({});
  page = $state(0);
  cursor = $state(0);
  done = $state<api.SuggestDone | null>(null);
  provider = $state<api.LLMProvider | null>(null);
  applying = $state(false);
  applyError = $state("");
  applyFailures = $state<ApplyFailure[]>([]);
  /** Seconds since the job started (ticks while it runs). */
  elapsed = $state(0);
  private timer: ReturnType<typeof setInterval> | undefined;

  private gen = 0;
  private offs: (() => void)[] = [];

  get analysed(): number {
    return Object.keys(this.results).length;
  }
  get total(): number {
    return this.targets.length;
  }
  get running(): boolean {
    return this.phase === "starting" || this.phase === "running";
  }
  get current(): Target | undefined {
    return this.targets[this.page];
  }
  get currentResult(): api.SuggestResult | undefined {
    const t = this.current;
    return t ? this.results[t.mid] : undefined;
  }
  get cloud(): boolean {
    return !!this.provider && this.provider.kind !== "local";
  }

  /** Ticked label names per email, only for emails with a result and a tick. */
  accepted(): Record<string, string[]> {
    const out: Record<string, string[]> = {};
    for (const t of this.targets) {
      const r = this.results[t.mid];
      if (!r?.ok || this.applied[t.mid]) continue;
      const t1 = this.ticks[t.mid] ?? {};
      const names = (r.labels ?? [])
        .filter((l) => t1[labelKey(l.name)])
        .map((l) => l.name);
      if (names.length > 0) out[t.mid] = names;
    }
    return out;
  }
  get tickedCount(): number {
    return Object.values(this.accepted()).reduce((n, a) => n + a.length, 0);
  }
  isTicked(mid: string, name: string): boolean {
    return !!this.ticks[mid]?.[labelKey(name)];
  }

  /** Open the popup for the current targets (selection, open message, cursor). */
  trigger(headers: api.MessageHeader[] = actions.targets()): void {
    if (this.open) return;
    if (headers.length === 0) return ui.flash("No message selected");
    const ready = headers.filter((h) => h.internalId);
    if (ready.length === 0)
      return ui.flash("These messages aren't ready yet. Try again after sync.");
    if (ready.length > MAX_SUGGEST)
      return ui.flash(
        `Suggestions work on up to ${MAX_SUGGEST} emails at a time. Select fewer (you have ${ready.length}).`,
      );
    void this.start(ready);
  }

  async start(headers: api.MessageHeader[]): Promise<void> {
    const gen = ++this.gen;
    this.reset();
    this.open = true;
    this.startClock();
    this.targets = headers.map((h) => ({
      mid: h.internalId as string,
      header: h,
    }));
    const mids = this.targets.map((t) => t.mid);

    // Subscribe before the call: the first results can beat the job id.
    const early: api.SuggestResult[] = [];
    let earlyDone: api.SuggestDone | null = null;
    let known = false;
    this.offs.push(
      api.onEvent<api.SuggestResult>("suggest:result", (r) => {
        if (gen !== this.gen) return;
        if (!known) early.push(r);
        else if (r.jobId === this.jobId) this.take(r);
      }),
      api.onEvent<api.SuggestDone>("suggest:done", (d) => {
        if (gen !== this.gen) return;
        if (!known) earlyDone = d;
        else if (d.jobId === this.jobId) this.finish(d);
      }),
    );

    let providers: api.LLMProvider[] = [];
    try {
      providers = (await api.listProviders()) ?? [];
    } catch {
      /* Go reports the real problem from Suggest. */
    }
    if (gen !== this.gen) return;
    if (providers.length === 0) {
      return this.fail(
        "no_provider",
        "No AI provider is set up yet. Add one in Settings.",
      );
    }
    this.provider = pickProvider(providers);

    let id: string;
    try {
      id = await api.suggest(mids, "");
    } catch (e) {
      if (gen !== this.gen) return;
      const msg = errText(e);
      if (msg.startsWith("cloud_disabled"))
        return this.fail(
          "cloud_disabled",
          "This cloud provider is turned off. Turn it on in Settings first; nothing was sent.",
        );
      return this.fail("error", msg);
    }
    if (gen !== this.gen) {
      // Closed while starting: stop the job that just began.
      void api.suggestCancel(id).catch(() => {});
      return;
    }
    this.jobId = id;
    this.phase = "running";
    known = true;
    // Late join: pick up anything that arrived before we knew the id.
    try {
      const job = await api.suggestStatus(id);
      if (gen !== this.gen) return;
      const named = pickProvider(providers, job.provider);
      if (named) this.provider = named;
      for (const r of job.results ?? []) this.take(r);
      if (job.state === "done")
        this.finish({ jobId: id, total: job.total, failed: job.failed });
    } catch {
      /* events still arrive */
    }
    for (const r of early) if (r.jobId === id) this.take(r);
    const d = earlyDone as api.SuggestDone | null;
    if (d && d.jobId === id) this.finish(d);
  }

  private fail(code: string, text: string): void {
    this.stopClock();
    this.fatal = { code, text };
    this.phase = "failed";
    this.unsubscribe();
  }

  private take(r: api.SuggestResult): void {
    if (this.results[r.mid] || !this.targets.some((t) => t.mid === r.mid))
      return;
    this.results = { ...this.results, [r.mid]: r };
    if (r.ok) {
      this.ticks = {
        ...this.ticks,
        [r.mid]: Object.fromEntries(
          (r.labels ?? []).map((l) => [labelKey(l.name), defaultTick(l)]),
        ),
      };
    }
  }

  private startClock(): void {
    this.stopClock();
    this.elapsed = 0;
    const t0 = Date.now();
    this.timer = setInterval(() => {
      this.elapsed = Math.floor((Date.now() - t0) / 1000);
    }, 1000);
  }
  private stopClock(): void {
    if (this.timer) clearInterval(this.timer);
    this.timer = undefined;
  }

  private finish(d: api.SuggestDone): void {
    this.stopClock();
    this.done = d;
    if (this.phase !== "cancelled") this.phase = "done";
    this.unsubscribe();
  }

  private unsubscribe(): void {
    this.offs.forEach((f) => f());
    this.offs = [];
  }

  private reset(): void {
    this.unsubscribe();
    this.fatal = null;
    this.phase = "starting";
    this.jobId = "";
    this.targets = [];
    this.results = {};
    this.ticks = {};
    this.applied = {};
    this.page = 0;
    this.cursor = 0;
    this.done = null;
    this.provider = null;
    this.applying = false;
    this.applyError = "";
    this.applyFailures = [];
  }

  // ---- navigation and ticking ----

  private labelCount(): number {
    return this.currentResult?.ok
      ? (this.currentResult.labels ?? []).length
      : 0;
  }
  moveCursor(d: number): void {
    const n = this.labelCount();
    if (n === 0) return;
    this.cursor = Math.max(0, Math.min(n - 1, this.cursor + d));
  }
  goPage(d: number): void {
    const p = Math.max(0, Math.min(this.total - 1, this.page + d));
    if (p !== this.page) {
      this.page = p;
      this.cursor = 0;
    }
  }
  toggle(mid: string, name: string): void {
    if (this.applied[mid]) return;
    const k = labelKey(name);
    this.ticks = {
      ...this.ticks,
      [mid]: { ...this.ticks[mid], [k]: !this.ticks[mid]?.[k] },
    };
  }
  toggleCursor(): void {
    const t = this.current;
    const l = this.currentResult?.labels?.[this.cursor];
    if (t && l) this.toggle(t.mid, l.name);
  }
  setAll(mid: string, on: boolean): void {
    if (this.applied[mid]) return;
    const r = this.results[mid];
    if (!r?.ok) return;
    this.ticks = {
      ...this.ticks,
      [mid]: Object.fromEntries(
        (r.labels ?? []).map((l) => [labelKey(l.name), on]),
      ),
    };
  }

  // ---- finishing ----

  /** Close and discard. A running job is cancelled in Go. */
  close(): void {
    const id = this.jobId;
    const was = this.running;
    this.gen++;
    this.stopClock();
    this.unsubscribe();
    if (was && id) void api.suggestCancel(id).catch(() => {});
    this.phase = "cancelled";
    this.open = false;
  }

  async accept(): Promise<void> {
    if (this.applying) return;
    const accepted = this.accepted();
    const mids = Object.keys(accepted);
    if (mids.length === 0) return;
    this.applying = true;
    this.applyError = "";
    this.applyFailures = [];
    const gen = this.gen;
    let res: api.SuggestApply;
    try {
      res = await api.applySuggestions(this.jobId, accepted);
    } catch (e) {
      if (gen !== this.gen) return;
      this.applying = false;
      this.applyError = errText(e);
      return;
    }
    if (gen !== this.gen) return;
    this.applying = false;
    const ok = new Set(res.applied ?? []);
    const failed = res.failed ?? [];
    // Split what was applied into label adds and folder moves.
    let labels = 0;
    let moved = 0;
    const folders: string[] = [];
    for (const m of ok) {
      const byName = new Map(
        (this.results[m]?.labels ?? []).map((l) => [labelKey(l.name), l]),
      );
      let movedHere = false;
      for (const n of accepted[m] ?? []) {
        const l = byName.get(labelKey(n));
        if (l?.target === "folder") {
          movedHere = true;
          if (!folders.includes(l.name)) folders.push(l.name);
        } else labels++;
      }
      if (movedHere) moved++;
    }
    const auditIds = res.auditIds ?? [];
    const created = res.createdLabels?.length ?? 0;
    if (ok.size > 0) {
      this.applied = {
        ...this.applied,
        ...Object.fromEntries([...ok].map((m) => [m, true as const])),
      };
      const parts: string[] = [];
      if (moved > 0)
        parts.push(
          `Moved ${plural(moved, "message")} to ${folders.join(", ")}`,
        );
      if (labels > 0)
        parts.push(
          moved > 0
            ? `added ${plural(labels, "label")}`
            : `Added ${plural(labels, "label")} to ${plural(ok.size, "message")}`,
        );
      let text = parts.join(", ");
      if (created > 0) text += ` (${plural(created, "new label")} created)`;
      actions.showToast(
        {
          kind: failed.length > 0 ? "error" : "ok",
          text:
            failed.length > 0
              ? `${text}. ${plural(failed.length, "message")} failed.`
              : text,
          undo: auditIds.length > 0,
          auditIds,
        },
        failed.length > 0 ? 16000 : 8000,
      );
    }
    void mail.refreshHead();
    if (failed.length === 0) {
      if (ok.size === 0) this.applyError = "Nothing was applied.";
      else return this.close();
      return;
    }
    this.applyFailures = failed.map((f) => ({
      mid: f.mid,
      text: codeText(f.code ?? "", f.message),
      detail: f.message ?? "",
    }));
  }

  subjectOf(mid: string): string {
    const t = this.targets.find((x) => x.mid === mid);
    return t?.header.subject || "(no subject)";
  }
}

export const suggest = new Suggest();
