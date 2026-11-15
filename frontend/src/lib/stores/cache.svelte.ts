import * as api from "../api";

/** At most one reload this often while a sync is running. */
export const THROTTLE_MS = 5000;

class Cache {
  info = $state<api.CacheInfo | null>(null);
  private last = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private busy = false;
  private offs: (() => void)[] = [];

  /** Load now. On any error the item is hidden (info becomes null). */
  async refresh(): Promise<void> {
    if (this.busy) return;
    this.busy = true;
    this.last = Date.now();
    try {
      this.info = await api.cacheInfo();
    } catch {
      this.info = null;
    } finally {
      this.busy = false;
    }
  }

  /** Reload soon, but never more than once per THROTTLE_MS. */
  request(): void {
    if (this.timer !== undefined) return;
    const wait = Math.max(0, this.last + THROTTLE_MS - Date.now());
    if (wait === 0) {
      void this.refresh();
      return;
    }
    this.timer = setTimeout(() => {
      this.timer = undefined;
      void this.refresh();
    }, wait);
  }

  start(): void {
    this.stop();
    void this.refresh();
    this.offs = [
      api.onEvent("sync:progress", () => this.request()),
      // A finished run always reloads, even inside the throttle window.
      api.onEvent("sync:done", () => {
        clearTimeout(this.timer);
        this.timer = undefined;
        void this.refresh();
      }),
    ];
  }

  stop(): void {
    this.offs.forEach((f) => f());
    this.offs = [];
    clearTimeout(this.timer);
    this.timer = undefined;
  }

  reset(): void {
    this.stop();
    this.info = null;
    this.last = 0;
    this.busy = false;
  }
}

export const cache = new Cache();
