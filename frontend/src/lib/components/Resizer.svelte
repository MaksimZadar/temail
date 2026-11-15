<script lang="ts">
  let {
    label,
    value,
    min,
    max,
    onchange,
    side = "right",
  }: {
    label: string;
    value: number;
    min: number;
    max: number;
    onchange: (w: number) => void;
    /** Which side of the handle the resized pane is on. */
    side?: "left" | "right";
  } = $props();

  let dragging = $state(false);

  function down(e: PointerEvent): void {
    dragging = true;
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    e.preventDefault();
  }
  function move(e: PointerEvent): void {
    if (!dragging) return;
    const el = e.currentTarget as HTMLElement;
    const pane = (
      side === "left" ? el.nextElementSibling : el.previousElementSibling
    ) as HTMLElement | null;
    if (!pane) return;
    const r = pane.getBoundingClientRect();
    onchange(side === "left" ? r.right - e.clientX : e.clientX - r.left);
  }
  function up(): void {
    dragging = false;
  }
  function key(e: KeyboardEvent): void {
    const step = e.shiftKey ? 40 : 10;
    const dir = side === "left" ? -1 : 1;
    if (e.key === "ArrowLeft") onchange(value - step * dir);
    else if (e.key === "ArrowRight") onchange(value + step * dir);
    else return;
    e.preventDefault();
    e.stopPropagation();
  }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions, a11y_no_noninteractive_tabindex -->
<div
  class="handle"
  class:dragging
  role="separator"
  aria-orientation="vertical"
  aria-label={label}
  aria-valuemin={min}
  aria-valuemax={max}
  aria-valuenow={Math.round(value)}
  tabindex="0"
  onpointerdown={down}
  onpointermove={move}
  onpointerup={up}
  onpointercancel={up}
  onkeydown={key}
></div>

<style>
  .handle {
    width: 5px;
    margin: 0 -2px;
    z-index: 2;
    cursor: col-resize;
    background: var(--line);
    background-clip: content-box;
    padding: 0 2px;
    touch-action: none;
  }
  .handle:hover,
  .handle.dragging {
    background-color: var(--accent);
  }
</style>
