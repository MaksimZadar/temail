import * as api from "../api";

class Sync {
  running = $state(false);
  progress = $state<api.ProgressEvent | null>(null);
  result = $state<api.SyncResult | null>(null);
  error = $state("");
  private offs: (() => void)[] = [];

  /** Text for the status bar and the placeholder. */
  get text(): string {
    if (this.error) return "sync failed";
    if (this.running && this.progress) {
      const p = this.progress;
      return p.headersTotal > 0
        ? `syncing ${p.mailbox} ${p.headersDone}/${p.headersTotal}`
        : `syncing ${p.mailbox}`.trim();
    }
    if (this.running) return "syncing";
    if (this.result?.mailboxErrors?.length)
      return "synced, some folders failed";
    if (this.result) return "synced";
    return "idle";
  }

  get fraction(): number | null {
    const p = this.progress;
    return this.running && p && p.headersTotal > 0
      ? p.headersDone / p.headersTotal
      : null;
  }

  async start(): Promise<void> {
    this.stop();
    this.offs = [
      api.onEvent<api.ProgressEvent>("sync:progress", (p) => {
        this.running = true;
        this.error = "";
        this.progress = p;
      }),
      api.onEvent<api.SyncResult>("sync:done", (r) => {
        this.running = false;
        this.progress = null;
        this.result = r;
        this.error = "";
      }),
      api.onEvent<{ message: string }>("sync:error", (e) => {
        this.running = false;
        this.progress = null;
        this.error = e.message || "Sync failed.";
      }),
    ];
    try {
      const s = await api.syncStatus();
      this.running = this.running || s.running;
      this.result ??= s.lastResult;
      if (s.lastError && !this.result) this.error = s.lastError;
    } catch {
      /* status is optional; events still arrive */
    }
  }

  stop(): void {
    this.offs.forEach((f) => f());
    this.offs = [];
  }
}

export const sync = new Sync();
