export interface Cmd {
  name: string;
  /** Extra words that also match (for example "sync" for Refresh). */
  aliases?: string[];
  run: () => void;
}

export interface Ranked {
  cmd: Cmd;
  /** Indexes of `name` to highlight. */
  hits: number[];
}

function subsequence(hay: string, needle: string): number[] | null {
  const hits: number[] = [];
  let from = 0;
  for (const ch of needle) {
    const i = hay.indexOf(ch, from);
    if (i < 0) return null;
    hits.push(i);
    from = i + 1;
  }
  return hits;
}

/**
 * Best matches first. A substring of the name beats a substring of an alias,
 * which beats a loose in-order match (fuzzy). Empty query keeps the order.
 */
export function rank(cmds: Cmd[], query: string): Ranked[] {
  const q = query.trim().toLowerCase();
  if (!q) return cmds.map((cmd) => ({ cmd, hits: [] }));
  const scored: { r: Ranked; score: number; order: number }[] = [];
  cmds.forEach((cmd, order) => {
    const name = cmd.name.toLowerCase();
    const at = name.indexOf(q);
    if (at >= 0) {
      const hits = Array.from({ length: q.length }, (_, i) => at + i);
      // Earlier and word-start matches rank higher.
      const wordStart = at === 0 || /\W/.test(name[at - 1]);
      scored.push({ r: { cmd, hits }, score: (wordStart ? 0 : 5) + at, order });
      return;
    }
    if ((cmd.aliases ?? []).some((a) => a.toLowerCase().includes(q))) {
      scored.push({ r: { cmd, hits: [] }, score: 100, order });
      return;
    }
    const fuzzy = q.length >= 2 ? subsequence(name, q) : null;
    if (fuzzy) {
      const spread = fuzzy[fuzzy.length - 1] - fuzzy[0];
      scored.push({ r: { cmd, hits: fuzzy }, score: 200 + spread, order });
    }
  });
  return scored
    .sort((a, b) => a.score - b.score || a.order - b.order)
    .map((s) => s.r);
}

/** Split a name into plain and highlighted pieces. */
export function pieces(
  name: string,
  hits: number[],
): { text: string; hit: boolean }[] {
  const set = new Set(hits);
  const out: { text: string; hit: boolean }[] = [];
  for (let i = 0; i < name.length; i++) {
    const hit = set.has(i);
    const last = out[out.length - 1];
    if (last && last.hit === hit) last.text += name[i];
    else out.push({ text: name[i], hit });
  }
  return out;
}
