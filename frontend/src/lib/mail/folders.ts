import type { MailboxDTO, MailboxKind } from "../api";

const RANK: Record<MailboxKind, number> = {
  inbox: 0,
  drafts: 1,
  sent: 2,
  archive: 3,
  spam: 4,
  trash: 5,
  folder: 6,
  label: 7,
};

/** Inbox, Drafts, Sent, Archive, Spam, Trash, then folders, then labels. */
export function orderFolders(boxes: MailboxDTO[]): MailboxDTO[] {
  return [...boxes].sort(
    (a, b) =>
      (RANK[a.kind] ?? 99) - (RANK[b.kind] ?? 99) ||
      a.displayName.localeCompare(b.displayName, undefined, {
        sensitivity: "base",
      }),
  );
}

export const GROUPS: { title: string; kinds: MailboxKind[] }[] = [
  {
    title: "",
    kinds: ["inbox", "drafts", "sent", "archive", "spam", "trash"],
  },
  { title: "Folders", kinds: ["folder"] },
  { title: "Labels", kinds: ["label"] },
];

export function findByKind(
  boxes: MailboxDTO[],
  kind: MailboxKind,
): MailboxDTO | undefined {
  return boxes.find((b) => b.kind === kind);
}
