export type Pane = "folders" | "list" | "view";

const KEY = "temail.layout";
export const NARROW_BELOW = 800;
export const MIN = { folders: 150, list: 280, view: 320 };
export const MAX = { folders: 360, list: 720 };

interface Saved {
  folderW: number;
  listW: number;
  folderVisible: boolean;
}

function load(): Partial<Saved> {
  try {
    return JSON.parse(localStorage.getItem(KEY) ?? "{}") as Partial<Saved>;
  } catch {
    return {};
  }
}

const clamp = (n: number, lo: number, hi: number): number =>
  Math.min(hi, Math.max(lo, n));

class Layout {
  folderW = $state(210);
  listW = $state(400);
  folderVisible = $state(true);
  focus = $state<Pane>("list");
  width = $state(typeof window === "undefined" ? 1280 : window.innerWidth);

  constructor() {
    const s = load();
    if (typeof s.folderW === "number")
      this.folderW = clamp(s.folderW, MIN.folders, MAX.folders);
    if (typeof s.listW === "number")
      this.listW = clamp(s.listW, MIN.list, MAX.list);
    if (typeof s.folderVisible === "boolean")
      this.folderVisible = s.folderVisible;
    else if (this.width < 1000) this.folderVisible = false;
  }

  get narrow(): boolean {
    return this.width < NARROW_BELOW;
  }

  /** Panes on screen, left to right. In narrow mode only the focused one. */
  get visible(): Pane[] {
    if (this.narrow) return [this.focus];
    return this.folderVisible ? ["folders", "list", "view"] : ["list", "view"];
  }

  private persist(): void {
    try {
      localStorage.setItem(
        KEY,
        JSON.stringify({
          folderW: this.folderW,
          listW: this.listW,
          folderVisible: this.folderVisible,
        } satisfies Saved),
      );
    } catch {
      /* private mode etc.: layout just isn't remembered */
    }
  }

  setFolderW(w: number): void {
    this.folderW = clamp(w, MIN.folders, MAX.folders);
    this.persist();
  }
  setListW(w: number): void {
    const room =
      this.width - (this.folderVisible ? this.folderW : 0) - MIN.view;
    this.listW = clamp(
      w,
      MIN.list,
      Math.max(MIN.list, Math.min(MAX.list, room)),
    );
    this.persist();
  }
  toggleFolders(): void {
    if (this.narrow) {
      this.focus = this.focus === "folders" ? "list" : "folders";
      return;
    }
    this.folderVisible = !this.folderVisible;
    if (!this.folderVisible && this.focus === "folders") this.focus = "list";
    this.persist();
  }
  /** Move focus to a pane, showing the folder pane if needed. */
  setFocus(p: Pane): void {
    if (p === "folders" && !this.narrow && !this.folderVisible) {
      this.folderVisible = true;
      this.persist();
    }
    this.focus = p;
  }
  setWidth(w: number): void {
    this.width = w;
  }
  /** Step left or right through the visible panes. */
  step(dir: -1 | 1): void {
    const order: Pane[] = ["folders", "list", "view"];
    const i = order.indexOf(this.focus) + dir;
    if (i >= 0 && i < order.length) this.setFocus(order[i]);
  }
}

export const layout = new Layout();
