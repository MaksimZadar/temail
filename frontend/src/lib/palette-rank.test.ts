import { describe, expect, it } from "vitest";
import { pieces, rank, type Cmd } from "./palette-rank";

const c = (name: string, aliases: string[] = []): Cmd => ({
  name,
  aliases,
  run: () => {},
});
const cmds = [
  c("Refresh mail", ["sync"]),
  c("Show keyboard shortcuts", ["help"]),
  c("Theme: dark"),
  c("Theme: light"),
  c("Go to Inbox"),
  c("Go to Sent"),
];

describe("rank", () => {
  it("keeps the order for an empty query", () => {
    expect(rank(cmds, "").map((r) => r.cmd.name)).toEqual(
      cmds.map((x) => x.name),
    );
  });
  it("matches substrings, case-insensitively, best first", () => {
    const got = rank(cmds, "THEME").map((r) => r.cmd.name);
    expect(got).toEqual(["Theme: dark", "Theme: light"]);
    expect(rank(cmds, "sent")[0].cmd.name).toBe("Go to Sent");
  });
  it("prefers a word-start match over a mid-word one", () => {
    const got = rank([c("Archive thing"), c("Mail search")], "ar").map(
      (r) => r.cmd.name,
    );
    expect(got[0]).toBe("Archive thing");
  });
  it("matches aliases", () => {
    expect(rank(cmds, "sync")[0].cmd.name).toBe("Refresh mail");
    expect(rank(cmds, "help")[0].cmd.name).toBe("Show keyboard shortcuts");
  });
  it("falls back to an in-order fuzzy match", () => {
    expect(rank(cmds, "skb")[0].cmd.name).toBe("Show keyboard shortcuts");
  });
  it("returns nothing when nothing matches", () => {
    expect(rank(cmds, "zzzz")).toEqual([]);
  });
  it("reports highlight positions", () => {
    const r = rank(cmds, "inbox")[0];
    expect(pieces(r.cmd.name, r.hits)).toEqual([
      { text: "Go to ", hit: false },
      { text: "Inbox", hit: true },
    ]);
  });
});
