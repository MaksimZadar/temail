import type {
  ActionFailure,
  ActionResult,
  ActionType,
  MailboxDTO,
  MailboxRef,
} from "../api";
import { rank, type Cmd, type Ranked } from "../palette-rank";

/** Go refuses more than this many messages per Apply call. */
export const MAX_BATCH = 100;

export function chunk<T>(items: T[], size = MAX_BATCH): T[][] {
  const out: T[][] = [];
  for (let i = 0; i < items.length; i += size)
    out.push(items.slice(i, i + size));
  return out;
}

const plural = (n: number, one: string, many: string): string =>
  `${n} ${n === 1 ? one : many}`;

const PAST: Partial<Record<ActionType, string>> = {
  mark_read: "Marked as read",
  mark_unread: "Marked as unread",
  star: "Starred",
  unstar: "Unstarred",
  archive: "Archived",
  trash: "Moved to Trash",
  spam: "Marked as spam",
  not_spam: "Marked as not spam",
};

/** "Archived 3 messages", "Moved 1 message to Receipts". */
export function doneText(
  type: ActionType,
  count: number,
  param?: string,
): string {
  const n = plural(count, "message", "messages");
  if (type === "move") return `Moved ${n} to ${param ?? "folder"}`;
  if (type === "add_label") return `Added label ${param ?? ""} to ${n}`.trim();
  if (type === "remove_label")
    return `Removed label ${param ?? ""} from ${n}`.trim();
  if (type === "trash") return `Moved ${n} to Trash`;
  return `${PAST[type] ?? "Done"} ${n}`;
}

/** "2 of 5 failed: <reason>" (the most common reason, plus "and more"). */
export function failureText(failed: ActionFailure[], total: number): string {
  if (failed.length === 0) return "";
  const counts = new Map<string, number>();
  for (const f of failed)
    counts.set(f.message, (counts.get(f.message) ?? 0) + 1);
  const [top] = [...counts.entries()].sort((a, b) => b[1] - a[1]);
  const more = counts.size > 1 ? ` (and ${counts.size - 1} other reasons)` : "";
  return `${failed.length} of ${total} failed: ${top[0] || "unknown error"}${more}`;
}

/** Merge several chunk results into one. */
export function mergeResults(rs: ActionResult[]): ActionResult {
  const applied = rs.flatMap((r) => r.applied ?? []);
  const failed = rs.flatMap((r) => r.failed ?? []);
  return {
    applied,
    failed,
    appliedCount: applied.length,
    failedCount: failed.length,
  };
}

export const isNotUndoable = (e: unknown): boolean =>
  /no inverse|not undoable|can'?t be undone/i.test(
    e instanceof Error ? e.message : String(e ?? ""),
  );

/** Folders a message can be moved to: Folders/* plus Archive, Trash, Spam. */
export function moveTargets(folders: MailboxDTO[]): MailboxDTO[] {
  return folders.filter(
    (f) =>
      f.kind === "folder" ||
      f.kind === "archive" ||
      f.kind === "trash" ||
      f.kind === "spam",
  );
}

export function filterFolders(folders: MailboxDTO[], q: string): Ranked[] {
  const cmds: Cmd[] = moveTargets(folders).map((f) => ({
    name: f.displayName,
    aliases: [f.name],
    run: () => {},
  }));
  return rank(cmds, q);
}

/** Go wants the bare label name; the mailbox is "Labels/<name>". */
export const labelParam = (mailbox: string): string =>
  mailbox.replace(/^Labels\//, "");

/** The action for a picked destination: system folders have their own type. */
export function moveActionFor(f: MailboxDTO): {
  type: ActionType;
  params: { folder?: string };
} {
  if (f.kind === "trash") return { type: "trash", params: {} };
  if (f.kind === "spam") return { type: "spam", params: {} };
  if (f.kind === "archive") return { type: "archive", params: {} };
  return { type: "move", params: { folder: f.name } };
}

export function labelsOf(folders: MailboxDTO[]): MailboxDTO[] {
  return folders.filter((f) => f.kind === "label");
}

/**
 * Per label: "all" when every target message has it, "some" when only part do,
 * "none" otherwise.
 */
export type LabelState = "all" | "some" | "none";
export function labelStates(
  labels: MailboxDTO[],
  targets: { mailboxes?: MailboxRef[] | null }[],
): Record<string, LabelState> {
  const out: Record<string, LabelState> = {};
  for (const l of labels) {
    const n = targets.filter((t) =>
      (t.mailboxes ?? []).some((r) => r.name === l.name),
    ).length;
    out[l.name] = n === 0 ? "none" : n === targets.length ? "all" : "some";
  }
  return out;
}

/** Which labels to add and remove to go from `before` to the picked `after`. */
export function diffLabels(
  before: Record<string, LabelState>,
  after: Record<string, boolean>,
): { add: string[]; remove: string[] } {
  const add: string[] = [];
  const remove: string[] = [];
  for (const [name, on] of Object.entries(after)) {
    const was = before[name] ?? "none";
    if (on && was !== "all") add.push(name);
    else if (!on && was !== "none") remove.push(name);
  }
  return { add, remove };
}
