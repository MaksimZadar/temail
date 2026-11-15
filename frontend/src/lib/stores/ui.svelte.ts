import type { Binding, Context } from "../keys/keymap";

export type Overlay = "none" | "help" | "palette" | "sync";
export type Theme = "auto" | "light" | "dark";

class UI {
  overlay = $state<Overlay>("none");
  context = $state<Context>("wizard");
  pending = $state<string[]>([]);
  count = $state<number | undefined>(undefined);
  whichKey = $state(false);
  hint = $state("");
  paletteQuery = $state("");
  theme = $state<Theme>("auto");
  /** Set by the app: cancel a pending prefix, or run a which-key entry. */
  cancelKeys: () => void = () => {};
  triggerBinding: (b: Binding) => void = () => {};
  private actions = new Map<string, (count: number | undefined) => void>();
  private hintTimer: ReturnType<typeof setTimeout> | undefined;
  private wkTimer: ReturnType<typeof setTimeout> | undefined;

  get mode(): "NORMAL" | "COMMAND" | "HELP" {
    return this.overlay === "palette"
      ? "COMMAND"
      : this.overlay === "help"
        ? "HELP"
        : "NORMAL";
  }

  /** Register a handler for an action id. Returns a function that removes it. */
  register(id: string, fn: (count: number | undefined) => void): () => void {
    this.actions.set(id, fn);
    return () => {
      if (this.actions.get(id) === fn) this.actions.delete(id);
    };
  }

  run(b: Binding, count: number | undefined): boolean {
    const fn = this.actions.get(b.action);
    if (!fn) return false;
    fn(count);
    return true;
  }

  setPending(p: string[], count: number | undefined): void {
    this.pending = p;
    this.count = count;
    clearTimeout(this.wkTimer);
    this.whichKey = false;
    if (p.length > 0)
      this.wkTimer = setTimeout(() => (this.whichKey = true), 400);
  }

  flash(text: string): void {
    this.hint = text;
    clearTimeout(this.hintTimer);
    this.hintTimer = setTimeout(() => (this.hint = ""), 2500);
  }

  setTheme(t: Theme): void {
    this.theme = t;
    if (typeof document === "undefined") return;
    if (t === "auto") document.documentElement.removeAttribute("data-theme");
    else document.documentElement.dataset.theme = t;
  }
}

export const ui = new UI();
