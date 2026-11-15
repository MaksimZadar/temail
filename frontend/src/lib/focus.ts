const FOCUSABLE =
  'button:not([disabled]), [href], input:not([disabled]), select, textarea, [tabindex]:not([tabindex="-1"])';

/** Svelte action: keep Tab inside a dialog. Other keys are left alone. */
export function trapFocus(node: HTMLElement): { destroy(): void } {
  function onkeydown(e: KeyboardEvent): void {
    if (e.key !== "Tab") return;
    const items = [...node.querySelectorAll<HTMLElement>(FOCUSABLE)];
    if (items.length === 0) {
      e.preventDefault();
      node.focus();
      return;
    }
    const first = items[0];
    const last = items[items.length - 1];
    const active = document.activeElement;
    if (e.shiftKey && (active === first || active === node)) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && active === last) {
      e.preventDefault();
      first.focus();
    }
  }
  node.addEventListener("keydown", onkeydown);
  return { destroy: () => node.removeEventListener("keydown", onkeydown) };
}
