import type { SuggestLabel } from "../api";

/** The most emails one suggestion job takes (matches Go). */
export const MAX_SUGGEST = 25;

/** Friendly copy for the stable failure codes Go returns. Never matches text. */
const CODES: Record<string, string> = {
  not_cached: "This email isn't downloaded yet. Try again after sync.",
  cancelled: "Stopped before this email was analysed.",
  too_large: "This email is too long to analyse.",
  invalid_output: "The AI gave an answer temail couldn't use.",
  cache_error: "Couldn't read this email from the local cache.",
  create_failed: "Couldn't create the new label on Proton.",
  move_failed: "Couldn't move this email to the folder.",
  apply_failed: "Couldn't add the label to this email.",
  rejected: "This label isn't one the AI suggested.",
  cloud_disabled: "This cloud provider is turned off. Turn it on in Settings.",
};

export function codeText(code: string, fallback = ""): string {
  if (code && CODES[code]) return CODES[code];
  return fallback || "Something went wrong.";
}

/** Suggestions are pre-ticked when sure enough and the label already exists. */
export const PRETICK = 0.6;
export const defaultTick = (l: SuggestLabel): boolean =>
  (l.target ?? (l.existing ? "label" : "new")) !== "new" &&
  l.confidence >= PRETICK;

/** Shorten long server text for the list; the full text goes in a tooltip. */
export const clip = (s: string, n = 120): string =>
  s.length > n ? `${s.slice(0, n - 1).trimEnd()}…` : s;

export function elapsed(sec: number): string {
  const s = Math.max(0, Math.floor(sec));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

export const labelKey = (name: string): string => name.toLowerCase();

export function confidenceText(c: number): string {
  const pct = Math.round(Math.max(0, Math.min(1, c)) * 100);
  const word = c >= 0.8 ? "High" : c >= 0.5 ? "Medium" : "Low";
  return `${word} confidence (${pct}%)`;
}

export const plural = (n: number, one: string, many = `${one}s`): string =>
  `${n} ${n === 1 ? one : many}`;
