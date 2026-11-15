import { continuations, keymap, type Binding, type Context } from "./keymap";

export interface KeyEventLike {
  key: string;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
  isComposing?: boolean;
  keyCode?: number;
  target: EventTarget | null;
  preventDefault(): void;
}

export interface DispatcherOptions {
  contexts: () => readonly Context[];
  /** Run an action. Return false when nothing handles it. */
  run: (binding: Binding, count: number | undefined) => boolean;
  /** An overlay (help, palette) is open: only Escape is handled, to close it. */
  overlayOpen: () => boolean;
  closeOverlay: () => void;
  /** Other keys while an overlay is open (for example `q`). True if handled. */
  overlayKey?: (token: string) => boolean;
  onPendingChange?: (pending: string[], count: number | undefined) => void;
  onHint?: (text: string) => void;
}

const MODIFIER_KEYS = new Set(["Shift", "Control", "Alt", "Meta", "CapsLock"]);
// Keys that act on the focused control themselves.
const NATIVE_ENTER = new Set(["BUTTON", "A", "SUMMARY", "SELECT"]);

export function isEditable(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el || typeof el.tagName !== "string") return false;
  const tag = el.tagName;
  return (
    tag === "INPUT" ||
    tag === "TEXTAREA" ||
    tag === "SELECT" ||
    el.isContentEditable === true
  );
}

/** Turn a keyboard event into a token like "j", "G", "C-d" or "Enter". */
export function tokenOf(e: KeyEventLike): string | null {
  if (MODIFIER_KEYS.has(e.key)) return null;
  if (e.ctrlKey) return e.key.length === 1 ? `C-${e.key.toLowerCase()}` : e.key;
  return e.key;
}

export class Dispatcher {
  private pending: string[] = [];
  private count: number | undefined;

  constructor(private readonly o: DispatcherOptions) {}

  get isPending(): boolean {
    return this.pending.length > 0 || this.count !== undefined;
  }

  /** Run a binding the user picked with the mouse (a which-key entry). */
  trigger(b: Binding): void {
    const count = this.count;
    this.reset();
    if (!this.o.run(b, count)) this.o.onHint?.("Not available yet");
  }

  /** Drop any pending prefix or count. */
  reset(): void {
    this.pending = [];
    this.count = undefined;
    this.o.onPendingChange?.([], undefined);
  }

  /** Returns true if the event was consumed. */
  handle(e: KeyEventLike): boolean {
    // IME composition and the OS/browser's own shortcuts are never ours.
    if (e.isComposing || e.keyCode === 229 || e.key === "Process") return false;
    if (e.metaKey || e.altKey) return false;
    const token = tokenOf(e);
    if (token === null) return false;

    if (this.o.overlayOpen()) {
      if (token === "Escape") {
        this.o.closeOverlay();
        e.preventDefault();
        return true;
      }
      // Typing in a field inside the overlay (the palette) is never ours.
      if (isEditable(e.target)) return false;
      if (this.o.overlayKey?.(token)) {
        e.preventDefault();
        return true;
      }
      return false;
    }

    if (isEditable(e.target)) {
      // Typing is never intercepted. Escape leaves the field.
      if (token === "Escape") {
        (e.target as HTMLElement).blur();
        e.preventDefault();
        return true;
      }
      return false;
    }
    // Enter on a focused button or link means "activate it".
    if (
      token === "Enter" &&
      NATIVE_ENTER.has((e.target as HTMLElement | null)?.tagName ?? "")
    ) {
      return false;
    }
    if (e.ctrlKey && !keymap.some((x) => x.keys[0] === token)) return false;

    // Count prefix: 1-9 first, then any digit.
    if (
      /^[0-9]$/.test(token) &&
      this.pending.length === 0 &&
      (token !== "0" || this.count)
    ) {
      this.count = (this.count ?? 0) * 10 + Number(token);
      this.arm();
      e.preventDefault();
      return true;
    }

    if (
      token === "Escape" &&
      (this.pending.length > 0 || this.count !== undefined)
    ) {
      this.reset();
      e.preventDefault();
      return true;
    }

    const seq = [...this.pending, token];
    const ctxs = this.o.contexts();
    const exact = keymap.find(
      (x) =>
        ctxs.includes(x.context) &&
        x.keys.length === seq.length &&
        seq.every((k, i) => x.keys[i] === k),
    );
    // A longer binding that shares this prefix wins only if no exact match.
    if (exact) {
      const count = this.count;
      this.reset();
      e.preventDefault();
      if (!this.o.run(exact, count)) this.o.onHint?.("Not available yet");
      return true;
    }
    if (continuations(seq, ctxs).length > 0) {
      this.pending = seq;
      this.arm();
      e.preventDefault();
      return true;
    }
    // Not a valid continuation: cancel the prefix and swallow the key, so a
    // typo never triggers some other action.
    const hadPending = this.isPending;
    this.reset();
    if (hadPending) e.preventDefault();
    return hadPending;
  }

  // A pending prefix has no timeout: the which-key popover stays up until a
  // valid key, Esc, a click elsewhere, or a click on one of its entries.
  private arm(): void {
    this.o.onPendingChange?.(this.pending, this.count);
  }
}
