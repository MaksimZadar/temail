import { describe, expect, it } from "vitest";

import { readFileSync } from "node:fs";
import { resolve } from "node:path";

const css = readFileSync(
  resolve(process.cwd(), "src/styles/tokens.css"),
  "utf8",
);

function block(startMarker: string): Record<string, string> {
  const start = css.indexOf(startMarker);
  const body = css.slice(css.indexOf("{", start) + 1, css.indexOf("}", start));
  return Object.fromEntries(
    [...body.matchAll(/--([\w-]+):\s*([^;]+);/g)].map((m) => [
      m[1],
      m[2].trim(),
    ]),
  );
}

const light = block(":root {");
const dark = block(':root[data-theme="dark"]');
const darkAuto = block(':root:not([data-theme="light"])');

function lum(hex: string): number {
  const n = hex.slice(1, 7);
  const [r, g, b] = [0, 2, 4].map((i) => {
    const c = parseInt(n.slice(i, i + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}
function ratio(a: string, b: string): number {
  const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
}

const required = [
  "bg",
  "bg-pane",
  "fg",
  "fg-dim",
  "line",
  "accent",
  "accent-fg",
  "sel-bg",
  "danger",
];

describe("theme tokens", () => {
  it.each([
    ["light", light],
    ["dark", dark],
    ["dark (system)", darkAuto],
  ])("%s defines every required token", (_n, t) => {
    for (const k of required) expect(t[k], k).toBeTruthy();
  });

  it("the system dark block matches the explicit dark block", () => {
    expect(darkAuto).toEqual(dark);
  });

  it("uses a violet accent", () => {
    for (const t of [light, dark]) {
      const h = t.accent;
      const [r, g, b] = [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
      expect(b).toBeGreaterThan(r);
      expect(b).toBeGreaterThan(g + 40);
    }
  });

  it.each([
    ["light", light],
    ["dark", dark],
  ])("%s meets WCAG AA for text pairs", (_n, t) => {
    expect(ratio(t.fg, t.bg)).toBeGreaterThanOrEqual(4.5);
    expect(ratio(t["fg-dim"], t.bg)).toBeGreaterThanOrEqual(4.5);
    expect(ratio(t["fg-dim"], t["bg-pane"])).toBeGreaterThanOrEqual(4.5);
    expect(ratio(t["fg-dim"], t["bg-raised"])).toBeGreaterThanOrEqual(4.5);
    expect(ratio(t.accent, t.bg)).toBeGreaterThanOrEqual(4.5);
    expect(ratio(t["accent-fg"], t.accent)).toBeGreaterThanOrEqual(4.5);
    expect(ratio(t.danger, t["bg-raised"])).toBeGreaterThanOrEqual(4.5);
  });
});
