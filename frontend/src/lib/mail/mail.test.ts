import { describe, expect, it } from "vitest";
import type { MailboxDTO } from "../api";
import { hdr } from "../../test-helpers";
import { orderFolders } from "./folders";
import {
  formatSize,
  isUnread,
  keyOf,
  matchesFilter,
  mergeHead,
  senderName,
} from "./format";
import { scrollTopFor, windowFor } from "./virtual";
import { needsConfirm, safeUrl } from "./links";
import { FRAME_CSP, buildSrcdoc, htmlToText, prepareHtml } from "./render";

const box = (name: string, kind: MailboxDTO["kind"]): MailboxDTO => ({
  name,
  displayName: name,
  kind,
  uidValidity: 1,
  messages: 0,
  bodies: 0,
  unseen: 0,
  lastSync: null,
});
describe("orderFolders", () => {
  it("puts system folders first in the agreed order, then folders, then labels", () => {
    const got = orderFolders([
      box("Labels/Work", "label"),
      box("Trash", "trash"),
      box("Folders/B", "folder"),
      box("Sent", "sent"),
      box("INBOX", "inbox"),
      box("Folders/A", "folder"),
      box("Spam", "spam"),
      box("Archive", "archive"),
      box("Drafts", "drafts"),
    ]).map((b) => b.name);
    expect(got).toEqual([
      "INBOX",
      "Drafts",
      "Sent",
      "Archive",
      "Spam",
      "Trash",
      "Folders/A",
      "Folders/B",
      "Labels/Work",
    ]);
  });
});

describe("virtual window", () => {
  it("renders only the visible rows plus overscan for 100k messages", () => {
    const w = windowFor(28 * 50_000, 560, 28, 100_000, 10);
    expect(w.total).toBe(2_800_000);
    expect(w.start).toBe(49_990);
    expect(w.end).toBe(50_000 + 20 + 10);
    expect(w.end - w.start).toBeLessThan(60);
    expect(w.offset).toBe(w.start * 28);
  });
  it("clamps at both ends and handles empty lists", () => {
    expect(windowFor(0, 280, 28, 5)).toMatchObject({ start: 0, end: 5 });
    expect(windowFor(0, 280, 28, 0)).toMatchObject({ start: 0, end: 0 });
    expect(windowFor(10_000_000, 280, 28, 100).end).toBe(100);
  });
  it("scrolls the least amount to reveal a row", () => {
    expect(scrollTopFor(5, 0, 280, 28)).toBe(0);
    expect(scrollTopFor(20, 0, 280, 28)).toBe(20 * 28 + 28 - 280);
    expect(scrollTopFor(1, 280, 280, 28)).toBe(28);
  });
});

describe("format", () => {
  it("detects unread from the \\Seen flag", () => {
    expect(isUnread(hdr(1))).toBe(true);
    expect(isUnread(hdr(1, { flags: ["\\Seen"] }))).toBe(false);
    expect(isUnread(hdr(1, { flags: null }))).toBe(true);
  });
  it("names the sender", () => {
    expect(senderName([{ name: "", email: "a@b.c" }])).toBe("a@b.c");
    expect(senderName(null)).toBe("(unknown)");
  });
  it("filters by subject or sender, case-insensitively", () => {
    expect(matchesFilter(hdr(1), "SUBJECT 1")).toBe(true);
    expect(matchesFilter(hdr(1), "ana@")).toBe(true);
    expect(matchesFilter(hdr(1), "zzz")).toBe(false);
    expect(matchesFilter(hdr(1), "  ")).toBe(true);
  });
  it("formats sizes", () => {
    expect(formatSize(500)).toBe("500 B");
    expect(formatSize(2048)).toBe("2.0 KB");
    expect(formatSize(5 * 1024 * 1024)).toBe("5.0 MB");
  });
  it("merges a fresh head without duplicates and keeps newest first", () => {
    const old = [hdr(3), hdr(2), hdr(1)];
    const merged = mergeHead(old, [hdr(5), hdr(4), hdr(3)]);
    expect(merged.map((h) => h.uid)).toEqual([5, 4, 3, 2, 1]);
  });
  it("returns the same array when nothing changed", () => {
    const old = [hdr(2), hdr(1)];
    expect(mergeHead(old, [hdr(2), hdr(1)])).toBe(old);
  });
  it("picks up flag changes", () => {
    const merged = mergeHead([hdr(1)], [hdr(1, { flags: ["\\Seen"] })]);
    expect(isUnread(merged[0])).toBe(false);
  });
});

describe("links", () => {
  it("only allows http, https and mailto", () => {
    expect(safeUrl("https://a.com/x")).toBe("https://a.com/x");
    expect(safeUrl("mailto:a@b.c")).toBe("mailto:a@b.c");
    expect(safeUrl("javascript:alert(1)")).toBeNull();
    expect(safeUrl("file:///etc/passwd")).toBeNull();
    expect(safeUrl("not a url")).toBeNull();
  });
  it("asks only when the shown address differs from the real one", () => {
    expect(needsConfirm("paypal.com", "https://evil.example/login")).toBe(true);
    expect(
      needsConfirm("https://www.paypal.com/a", "https://paypal.com/b"),
    ).toBe(false);
    expect(needsConfirm("Read more", "https://evil.example/")).toBe(false);
    expect(needsConfirm("", "https://a.com")).toBe(false);
  });
});

describe("render", () => {
  it("removes hrefs and collects safe links", () => {
    const p = prepareHtml(
      '<p><a href="https://a.com/x" target="_blank" onclick="x()">A</a> <a href="javascript:evil()">B</a></p>',
    );
    expect(p.html).not.toMatch(/href=/);
    expect(p.html).not.toMatch(/onclick/);
    expect(p.links).toEqual([{ text: "A", href: "https://a.com/x" }]);
  });
  it("drops scripts, frames, forms and base tags", () => {
    const p = prepareHtml(
      '<base href="https://x"><script>1</script><iframe src="x"></iframe><form><input></form><p>hi</p>',
    );
    expect(p.html).toBe("<p>hi</p>");
  });
  it("builds a document with a strict CSP", () => {
    const doc = buildSrcdoc("<p>x</p>");
    expect(doc).toContain(`content="${FRAME_CSP}"`);
    expect(FRAME_CSP).toBe(
      "default-src 'none'; style-src 'unsafe-inline'; img-src data:",
    );
    expect(doc).not.toMatch(/<script/i);
  });
  it("makes readable plain text from html", () => {
    expect(htmlToText("<p>Hello<br>there</p><p>Bye</p>")).toBe(
      "Hello\nthere\nBye",
    );
  });
});

describe("keyOf", () => {
  it("includes the uid validity so epochs never collide", () => {
    expect(keyOf(hdr(1))).not.toBe(keyOf(hdr(1, { uidValidity: 8 })));
  });
});
