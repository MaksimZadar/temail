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

const refNames = (h: MessageHeader): string =>
  (h.mailboxes ?? []).map((r) => r.name).join("\u0000");

/**
 * Merge a fresh first page into the loaded list. Same array if nothing changed.
 * Loaded messages that fall inside the fresh page's range but are missing from
 * it were removed on the server and are dropped. When `complete` is true the
 * fresh page is the whole folder, so everything not in it is dropped.
 */
export function mergeHead(
  existing: MessageHeader[],
  fresh: MessageHeader[],
  complete = false,
): MessageHeader[] {
  const freshKeys = new Set(fresh.map(keyOf));
  const oldest = fresh[fresh.length - 1];
  let changed = false;
  let kept = existing;
  if (complete) {
    kept = existing.filter((h) => freshKeys.has(keyOf(h)));
    changed = kept.length !== existing.length;
  } else if (oldest) {
    // The list is newest first, so only the front part can be compared.
    const limit = Date.parse(oldest.date);
    const head: MessageHeader[] = [];
    let i = 0;
    for (; i < existing.length; i++) {
      const h = existing[i];
      if (Date.parse(h.date) <= limit) break;
      if (freshKeys.has(keyOf(h))) head.push(h);
      else changed = true;
    }
    if (changed) kept = head.concat(existing.slice(i));
  }
  const byKey = new Map(kept.map((h) => [keyOf(h), h]));
  for (const f of fresh) {
    const old = byKey.get(keyOf(f));
    if (
      !old ||
      old.flags?.join() !== f.flags?.join() ||
      old.subject !== f.subject ||
      refNames(old) !== refNames(f)
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
