import { keyOf } from "../mail/format";
import { mail } from "./mail.svelte";

/**
 * Multi-select. `x` toggles the cursor row, `v` starts a range that follows
 * the cursor (like vim's visual mode), Esc clears. Keys are message keys, so
 * the selection survives refreshes; it is dropped when the folder changes.
 */
class Selection {
  private base = $state<Record<string, true>>({});
  anchor = $state<string | null>(null);
  private folder = $state<string | null>(null);

  get visual(): boolean {
    return this.anchor !== null;
  }

  private rangeKeys(): string[] {
    if (this.anchor === null || this.folder !== mail.current) return [];
    const a = mail.shown.findIndex((m) => keyOf(m) === this.anchor);
    const c = mail.cursorIndex;
    if (a < 0 || c < 0) return [];
    const [lo, hi] = a <= c ? [a, c] : [c, a];
    return mail.shown.slice(lo, hi + 1).map(keyOf);
  }

  /** Selected message keys, in list order. */
  keys = $derived.by(() => {
    if (this.folder !== mail.current) return [] as string[];
    const set = new Set<string>([
      ...Object.keys(this.base),
      ...this.rangeKeys(),
    ]);
    return mail.shown.map(keyOf).filter((k) => set.has(k));
  });
  count = $derived(this.keys.length);
  set = $derived(new Set(this.keys));

  has(key: string): boolean {
    return this.set.has(key);
  }

  private commit(): void {
    const b: Record<string, true> = {};
    for (const k of this.keys) b[k] = true;
    this.base = b;
    this.anchor = null;
  }

  /** `x`: toggle the cursor row. */
  toggle(): void {
    const k = mail.cursorKey;
    if (k === null) return;
    this.commit();
    this.folder = mail.current;
    const b = { ...this.base };
    if (b[k]) delete b[k];
    else b[k] = true;
    this.base = b;
  }

  /** `v`: start a range at the cursor, or end it (keeping what it covers). */
  visualToggle(): void {
    if (this.visual) return this.commit();
    if (mail.cursorKey === null) return;
    this.folder = mail.current;
    this.anchor = mail.cursorKey;
  }

  /** Ctrl-click: toggle any row. */
  toggleKey(k: string): void {
    this.commit();
    this.folder = mail.current;
    const b = { ...this.base };
    if (b[k]) delete b[k];
    else b[k] = true;
    this.base = b;
  }

  /** Shift-click: select from the cursor row to this one. */
  extendTo(k: string): void {
    if (mail.cursorKey === null) return this.toggleKey(k);
    this.commit();
    this.folder = mail.current;
    this.anchor = mail.cursorKey;
    mail.cursorKey = k;
    this.commit();
  }

  selectAll(): void {
    this.folder = mail.current;
    this.anchor = null;
    const b: Record<string, true> = {};
    for (const m of mail.shown) b[keyOf(m)] = true;
    this.base = b;
  }

  clear(): void {
    this.base = {};
    this.anchor = null;
  }
}

export const sel = new Selection();
