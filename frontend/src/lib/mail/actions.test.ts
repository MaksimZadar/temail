import { describe, expect, it } from "vitest";
import type { MailboxDTO } from "../api";
import {
  chunk,
  diffLabels,
  doneText,
  failureText,
  filterFolders,
  undoCodeText,
  friendlyFailure,
  labelStates,
  mergeResults,
  moveActionFor,
  labelParam,
} from "./actions";

const box = (name: string, kind: MailboxDTO["kind"], d = name): MailboxDTO => ({
  name,
  displayName: d,
  kind,
  messages: 0,
  unseen: 0,
  uidValidity: 1,
  bodies: 0,
  lastSync: null,
});
const boxes = [
  box("INBOX", "inbox", "Inbox"),
  box("Archive", "archive"),
  box("Trash", "trash"),
  box("Folders/Receipts", "folder", "Receipts"),
  box("Folders/Work", "folder", "Work"),
  box("Labels/Urgent", "label", "Urgent"),
];

describe("chunk", () => {
  it("splits 250 ids into 100, 100, 50", () => {
    const ids = Array.from({ length: 250 }, (_, i) => `m${i}`);
    expect(chunk(ids).map((c) => c.length)).toEqual([100, 100, 50]);
    expect(chunk([])).toEqual([]);
  });
});

describe("texts", () => {
  it("writes the done text", () => {
    expect(doneText("archive", 3)).toBe("Archived 3 messages");
    expect(doneText("archive", 1)).toBe("Archived 1 message");
    expect(doneText("move", 2, "Work")).toBe("Moved 2 messages to Work");
  });
  it("summarises failures", () => {
    const f = (message: string) => ({ mid: "a", mailbox: "INBOX", message });
    expect(failureText([f("offline"), f("offline")], 5)).toBe(
      "2 of 5 failed: offline",
    );
    expect(failureText([f("a"), f("b")], 2)).toContain("1 other reasons");
  });
  it("merges chunk results", () => {
    const r = mergeResults([
      { applied: ["a"], failed: null, appliedCount: 1, failedCount: 0 },
      {
        applied: null,
        failed: [{ mid: "b", mailbox: "", message: "x" }],
        appliedCount: 0,
        failedCount: 1,
      },
    ]);
    expect([r.appliedCount, r.failedCount]).toEqual([1, 1]);
  });
  it("has friendly copy for every undo code", () => {
    expect(undoCodeText("not_found")).toBe("Nothing to undo");
    expect(undoCodeText("already_undone")).toBe("Already undone");
    expect(undoCodeText("not_undoable")).toBe("This action can't be undone");
    expect(undoCodeText("diverged")).toMatch(/changed since then/);
  });
  it("rewrites a copy-only failure once", () => {
    const m =
      "archived copy created; original still in INBOX — try again after the folders finish syncing";
    expect(friendlyFailure(m)).toBe(
      "Created the copy, but the original is still in INBOX — it will be resolved after the folders sync",
    );
    expect(friendlyFailure("offline")).toBe("offline");
  });
  it("merges batch ids and audit ids", () => {
    const r = mergeResults([
      { applied: [], failed: null, appliedCount: 0, failedCount: 0, batchId: "b1", auditIds: [1, 2] },
      { applied: [], failed: null, appliedCount: 0, failedCount: 0, batchId: "b2", auditIds: [3] },
    ]);
    expect(r.auditIds).toEqual([1, 2, 3]);
  });
});

describe("pickers", () => {
  it("lists folders and system folders, not labels or the inbox", () => {
    const names = filterFolders(boxes, "").map((r) => r.cmd.name);
    expect(names).toEqual(["Archive", "Trash", "Receipts", "Work"]);
  });
  it("filters", () => {
    expect(filterFolders(boxes, "rec").map((r) => r.cmd.name)).toEqual([
      "Receipts",
    ]);
  });
  it("uses the dedicated action for system folders", () => {
    expect(moveActionFor(boxes[2]).type).toBe("trash");
    expect(moveActionFor(boxes[3])).toEqual({
      type: "move",
      params: { folder: "Folders/Receipts" },
    });
  });
  it("strips the Labels/ prefix", () => {
    expect(labelParam("Labels/Urgent")).toBe("Urgent");
  });
  it("diffs labels against the current state", () => {
    const labels = [boxes[5], box("Labels/Home", "label", "Home")];
    const ref = (n: string) => ({
      name: n,
      displayName: n,
      kind: "label" as const,
    });
    const before = labelStates(labels, [
      { mailboxes: [ref("Labels/Urgent")] },
      { mailboxes: [ref("Labels/Urgent"), ref("Labels/Home")] },
    ]);
    expect(before).toEqual({ "Labels/Urgent": "all", "Labels/Home": "some" });
    expect(
      diffLabels(before, { "Labels/Urgent": false, "Labels/Home": true }),
    ).toEqual({ add: ["Labels/Home"], remove: ["Labels/Urgent"] });
    expect(
      diffLabels(before, { "Labels/Urgent": true, "Labels/Home": false }),
    ).toEqual({ add: [], remove: ["Labels/Home"] });
  });
});
