export interface Window {
  /** First rendered row. */
  start: number;
  /** One past the last rendered row. */
  end: number;
  /** Pixel offset of the first rendered row. */
  offset: number;
  /** Height of the whole list. */
  total: number;
}

/** Which rows to render for a scroll position. Fixed row height. */
export function windowFor(
  scrollTop: number,
  viewport: number,
  rowHeight: number,
  count: number,
  overscan = 10,
): Window {
  const total = count * rowHeight;
  if (count === 0 || viewport <= 0)
    return { start: 0, end: 0, offset: 0, total };
  const first = Math.floor(Math.max(0, scrollTop) / rowHeight);
  const last = Math.ceil((Math.max(0, scrollTop) + viewport) / rowHeight);
  const start = Math.max(0, first - overscan);
  const end = Math.min(count, last + overscan);
  return { start, end, offset: start * rowHeight, total };
}

/** New scrollTop that makes row `index` visible with the least movement. */
export function scrollTopFor(
  index: number,
  scrollTop: number,
  viewport: number,
  rowHeight: number,
): number {
  const top = index * rowHeight;
  const bottom = top + rowHeight;
  if (top < scrollTop) return top;
  if (bottom > scrollTop + viewport) return bottom - viewport;
  return scrollTop;
}
