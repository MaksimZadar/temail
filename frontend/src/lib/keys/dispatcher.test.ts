import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { Dispatcher, type KeyEventLike } from "./dispatcher";
import { keymap, isReserved } from "./keymap";

function key(k: string, extra: Partial<KeyEventLike> = {}): KeyEventLike {
  return {
    key: k,
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    target: document.body,
    preventDefault: vi.fn(),
    ...extra,
  };
}

function setup(
  contexts: ("main" | "wizard" | "global")[] = ["main", "global"],
  overlay = false,
) {
  const run = vi.fn(() => true);
  const closeOverlay = vi.fn();
  const d = new Dispatcher({
    contexts: () => contexts,
    run,
    overlayOpen: () => overlay,
    closeOverlay,
    timeoutMs: 1000,
  });
  return { d, run, closeOverlay };
}

describe("Dispatcher", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("runs a single key", () => {
    const { d, run } = setup();
    expect(d.handle(key("j"))).toBe(true);
    expect(run).toHaveBeenCalledWith(
      expect.objectContaining({ action: "list.down" }),
      undefined,
    );
  });

  it("passes a count prefix", () => {
    const { d, run } = setup();
    d.handle(key("1"));
    d.handle(key("2"));
    d.handle(key("j"));
    expect(run).toHaveBeenCalledWith(
      expect.objectContaining({ action: "list.down" }),
      12,
    );
  });

  it("does not treat a leading 0 as a count", () => {
    const { d, run } = setup();
    expect(d.handle(key("0"))).toBe(false);
    expect(run).not.toHaveBeenCalled();
  });

  it("handles the g prefix and the count with G", () => {
    const { d, run } = setup();
    d.handle(key("g"));
    expect(run).not.toHaveBeenCalled();
    d.handle(key("i"));
    expect(run).toHaveBeenLastCalledWith(
      expect.objectContaining({ action: "goto.inbox" }),
      undefined,
    );
    d.handle(key("g"));
    d.handle(key("g"));
    expect(run).toHaveBeenLastCalledWith(
      expect.objectContaining({ action: "list.first" }),
      undefined,
    );
    d.handle(key("5"));
    d.handle(key("G"));
    expect(run).toHaveBeenLastCalledWith(
      expect.objectContaining({ action: "list.last" }),
      5,
    );
  });

  it("drops a stalled prefix after the timeout", () => {
    const { d, run } = setup();
    d.handle(key("g"));
    vi.advanceTimersByTime(1100);
    d.handle(key("i"));
    // "i" is now a fresh key (load images), not "g i" (inbox).
    expect(run).toHaveBeenCalledTimes(1);
    expect(run).toHaveBeenCalledWith(
      expect.objectContaining({ action: "view.images" }),
      undefined,
    );
  });

  it("Escape cancels a pending prefix without going back", () => {
    const { d, run } = setup(["main", "global"]);
    d.handle(key("g"));
    d.handle(key("Escape"));
    expect(run).not.toHaveBeenCalled();
  });

  it("ignores typing in inputs but Escape blurs", () => {
    const { d, run } = setup();
    const input = document.createElement("input");
    document.body.append(input);
    input.focus();
    expect(d.handle(key("j", { target: input }))).toBe(false);
    expect(d.handle(key("Enter", { target: input }))).toBe(false);
    expect(run).not.toHaveBeenCalled();
    expect(d.handle(key("Escape", { target: input }))).toBe(true);
    expect(document.activeElement).not.toBe(input);
    input.remove();
  });

  it("ignores IME composition and meta/alt combos", () => {
    const { d, run } = setup();
    expect(d.handle(key("j", { isComposing: true }))).toBe(false);
    expect(d.handle(key("Process", { keyCode: 229 }))).toBe(false);
    expect(d.handle(key("j", { metaKey: true }))).toBe(false);
    expect(d.handle(key("j", { altKey: true }))).toBe(false);
    expect(run).not.toHaveBeenCalled();
  });

  it("leaves Ctrl shortcuts it does not own to the browser", () => {
    const { d, run } = setup();
    expect(d.handle(key("c", { ctrlKey: true }))).toBe(false);
    expect(d.handle(key("d", { ctrlKey: true }))).toBe(true);
    expect(run).toHaveBeenCalledWith(
      expect.objectContaining({ action: "list.halfDown" }),
      undefined,
    );
  });

  it("lets Enter activate a focused button", () => {
    const { d, run } = setup(["wizard", "global"]);
    const btn = document.createElement("button");
    expect(d.handle(key("Enter", { target: btn }))).toBe(false);
    expect(run).not.toHaveBeenCalled();
    d.handle(key("Enter"));
    expect(run).toHaveBeenCalledWith(
      expect.objectContaining({ action: "wizard.next" }),
      undefined,
    );
  });

  it("only uses bindings from the active contexts", () => {
    const { d, run } = setup(["wizard", "global"]);
    expect(d.handle(key("j"))).toBe(false);
    expect(run).not.toHaveBeenCalled();
    d.handle(key("?"));
    expect(run).toHaveBeenCalledWith(
      expect.objectContaining({ action: "help.toggle" }),
      undefined,
    );
  });

  it("with an overlay open, only Escape is handled", () => {
    const { d, run, closeOverlay } = setup(["main", "global"], true);
    expect(d.handle(key("j"))).toBe(false);
    expect(d.handle(key("Escape"))).toBe(true);
    expect(closeOverlay).toHaveBeenCalled();
    expect(run).not.toHaveBeenCalled();
  });
});

describe("keymap table", () => {
  it("has no duplicate sequence in a context, except the shared Enter action", () => {
    const seen = new Set<string>();
    for (const b of keymap) {
      const id = `${b.context}:${b.keys.join(" ")}`;
      expect(seen.has(id), id).toBe(false);
      seen.add(id);
    }
  });

  it("marks later-phase keys as reserved", () => {
    const r = keymap.filter(
      (b) => b.action === "msg.archive" || b.action === "msg.reply",
    );
    expect(r.length).toBe(2);
    expect(r.every(isReserved)).toBe(true);
  });

  it("never shares a prefix with a one-key binding", () => {
    const single = new Set(
      keymap
        .filter((b) => b.keys.length === 1)
        .map((b) => `${b.context}:${b.keys[0]}`),
    );
    for (const b of keymap.filter((x) => x.keys.length > 1)) {
      expect(single.has(`${b.context}:${b.keys[0]}`), b.keys.join(" ")).toBe(
        false,
      );
    }
  });
});
