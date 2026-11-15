import type { Address, MessageHeader } from "../api";

export const keyOf = (h: {
  mailbox: string;
  uidValidity: number;
  uid: number;
}): string => `${h.mailbox}\u0000${h.uidValidity}\u0000${h.uid}`;

export const isUnread = (h: MessageHeader): boolean =>
  !(h.flags ?? []).some((f) => f.toLowerCase() === "\\seen");

export function senderName(from: Address[] | null): string {
  const a = from?.[0];
  if (!a) return "(unknown)";
  return a.name?.trim() || a.email || "(unknown)";
}

export const addressText = (list: Address[] | null): string =>
  (list ?? [])
    .map((a) => (a.name ? `${a.name} <${a.email}>` : a.email))
    .join(", ");

/** Today: time. This year: "12 Mar". Older: "12 Mar 2023". */
export function formatListDate(iso: string, now = new Date()): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  if (d.toDateString() === now.toDateString())
    return d.toLocaleTimeString(undefined, {
      hour: "2-digit",
      minute: "2-digit",
    });
  if (d.getFullYear() === now.getFullYear())
    return d.toLocaleDateString(undefined, { day: "numeric", month: "short" });
  return d.toLocaleDateString(undefined, {
    day: "numeric",
    month: "short",
    year: "numeric",
  });
}

export const formatFullDate = (iso: string): string => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? ""
    : d.toLocaleString(undefined, { dateStyle: "full", timeStyle: "short" });
};

export function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10240 ? 1 : 0)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

/** Does a message match a filter typed by the user (subject or sender)? */
export function matchesFilter(h: MessageHeader, q: string): boolean {
  const needle = q.trim().toLowerCase();
  if (!needle) return true;
  const hay = [h.subject, ...(h.from ?? []).flatMap((a) => [a.name, a.email])]
    .join("\n")
    .toLowerCase();
  return hay.includes(needle);
}

/** Merge a fresh first page into the loaded list. Same array if nothing changed. */
export function mergeHead(
  existing: MessageHeader[],
  fresh: MessageHeader[],
): MessageHeader[] {
  const byKey = new Map(existing.map((h) => [keyOf(h), h]));
  let changed = false;
  for (const f of fresh) {
    const old = byKey.get(keyOf(f));
    if (
      !old ||
      old.flags?.join() !== f.flags?.join() ||
      old.subject !== f.subject
    ) {
      byKey.set(keyOf(f), f);
      changed = true;
    }
  }
  if (!changed) return existing;
  return [...byKey.values()].sort(
    (a, b) => Date.parse(b.date) - Date.parse(a.date) || b.uid - a.uid,
  );
}

const UNITS = ["B", "KB", "MB", "GB", "TB"];

/** 1024-based size: "0 B", "1023 B", "1.0 KB", "12 MB", "1.2 GB". */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "0 B";
  let v = n;
  let u = 0;
  while (v >= 1024 && u < UNITS.length - 1) {
    v /= 1024;
    u++;
  }
  if (u === 0) return `${Math.round(v)} B`;
  let text = v < 10 ? v.toFixed(1) : String(Math.round(v));
  // 1023.96 KB rounds up to "1024": show it in the next unit instead.
  if (Number(text) >= 1024 && u < UNITS.length - 1) {
    v /= 1024;
    u++;
    text = v.toFixed(1);
  }
  return `${text} ${UNITS[u]}`;
}
