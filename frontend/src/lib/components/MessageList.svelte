<script lang="ts">
  import { tick } from "svelte";
  import { mail } from "../stores/mail.svelte";
  import { layout } from "../stores/layout.svelte";
  import { sync } from "../stores/sync.svelte";
  import { formatListDate, isUnread, keyOf, senderName } from "../mail/format";
  import { scrollTopFor, windowFor } from "../mail/virtual";

  const ROW = 28;

  let box = $state<HTMLDivElement>();
  let scrollTop = $state(0);
  let viewport = $state(400);

  const win = $derived(windowFor(scrollTop, viewport, ROW, mail.shown.length));
  const rows = $derived(mail.shown.slice(win.start, win.end));
  const focused = $derived(layout.focus === "list");
  const activeId = $derived(
    mail.cursorKey !== null && mail.cursorIndex >= 0
      ? `msg-${mail.cursorIndex}`
      : undefined,
  );

  $effect(() => {
    if (!box) return;
    const ro = new ResizeObserver(() => (viewport = box!.clientHeight || 400));
    ro.observe(box);
    viewport = box.clientHeight || 400;
    return () => ro.disconnect();
  });

  // Keep the cursor row on screen when it moves.
  $effect(() => {
    const i = mail.cursorIndex;
    if (!box || i < 0) return;
    const to = scrollTopFor(i, box.scrollTop, box.clientHeight, ROW);
    if (to !== box.scrollTop) box.scrollTop = to;
  });

  // New mail above the visible rows must not push what you're reading away.
  let anchor: { key: string; offset: number } | null = null;
  $effect(() => {
    const list = mail.shown;
    if (!box) return;
    if (anchor && box.scrollTop > 0) {
      const i = list.findIndex((m) => keyOf(m) === anchor!.key);
      if (i >= 0) {
        const to = i * ROW + anchor.offset;
        if (Math.abs(to - box.scrollTop) >= 1) box.scrollTop = to;
      }
    }
  });

  function onscroll(): void {
    scrollTop = box!.scrollTop;
    const i = Math.floor(scrollTop / ROW);
    const m = mail.shown[i];
    anchor = m ? { key: keyOf(m), offset: scrollTop - i * ROW } : null;
    if (mail.shown.length - (i + 20) < 60) void mail.loadPage(false);
  }

  function onkeydown(e: KeyboardEvent): void {
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    if (e.key === "ArrowDown") mail.moveCursor(1);
    else if (e.key === "ArrowUp") mail.moveCursor(-1);
    else if (e.key === "Home") mail.cursorTo(0);
    else if (e.key === "End") void mail.cursorToEnd();
    else if (e.key === "PageDown") mail.moveCursor(Math.floor(viewport / ROW));
    else if (e.key === "PageUp") mail.moveCursor(-Math.floor(viewport / ROW));
    else if (e.key === "Enter") void mail.openCursor();
    else return;
    e.preventDefault();
  }

  async function focusList(): Promise<void> {
    await tick();
    box?.focus({ preventScroll: true });
  }

  $effect(() => {
    if (focused) void focusList();
  });

  function click(i: number): void {
    layout.setFocus("view");
    void mail.open(mail.shown[i]);
  }
</script>

<section class="pane" class:focused aria-label="Messages">
  <div
    class="scroller"
    role="listbox"
    aria-label="Messages in {mail.folder?.displayName ?? 'folder'}"
    aria-activedescendant={activeId}
    aria-busy={mail.loading}
    data-pane="list"
    tabindex="0"
    bind:this={box}
    {onscroll}
    {onkeydown}
    onfocus={() => (layout.focus = "list")}
  >
    <div class="spacer" style:height="{win.total}px">
      <div class="rows" style:transform="translateY({win.offset}px)">
        {#each rows as m, k (keyOf(m))}
          {@const i = win.start + k}
          {@const unread = isUnread(m)}
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <div
            id="msg-{i}"
            class="row"
            class:unread
            class:cursor={i === mail.cursorIndex}
            class:open={keyOf(m) === mail.openKey}
            role="option"
            tabindex="-1"
            aria-selected={i === mail.cursorIndex}
            aria-posinset={i + 1}
            aria-setsize={mail.shown.length}
            aria-label="{unread ? 'Unread. ' : ''}{senderName(
              m.from,
            )}. {m.subject || '(no subject)'}. {formatListDate(m.date)}"
            style:height="{ROW}px"
            onclick={() => click(i)}
          >
            <span class="dot" aria-hidden="true"></span>
            <span class="from">{senderName(m.from)}</span>
            <span class="subject">{m.subject || "(no subject)"}</span>
            <span class="date">{formatListDate(m.date)}</span>
          </div>
        {/each}
      </div>
    </div>
    {#if mail.shown.length === 0}
      <div class="state" role="status">
        {#if mail.error}
          <p class="err">{mail.error}</p>
          <button class="btn" type="button" onclick={() => mail.reload()}
            >Try again</button
          >
        {:else if mail.loading}
          <p>Loading messages…</p>
        {:else if mail.filter.trim()}
          <p>No messages match “{mail.filter.trim()}”.</p>
          {#if mail.next}<p class="dim">
              Only loaded messages are searched.
            </p>{/if}
        {:else if sync.running}
          <p>Syncing. Messages will appear here.</p>
        {:else if !mail.current}
          <p>Pick a folder.</p>
        {:else}
          <p>This folder is empty.</p>
        {/if}
      </div>
    {/if}
  </div>
  {#if mail.loading && mail.shown.length > 0}
    <p class="more" aria-hidden="true">Loading more…</p>
  {/if}
</section>

<style>
  .pane {
    position: relative;
    display: flex;
    flex-direction: column;
    min-width: 0;
    height: 100%;
    background: var(--bg);
    border-top: 2px solid transparent;
  }
  .pane.focused {
    border-top-color: var(--accent);
  }
  .scroller {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    position: relative;
  }
  .scroller:focus-visible {
    outline-offset: -2px;
  }
  .rows {
    will-change: transform;
  }
  .row {
    display: grid;
    grid-template-columns: 0.5rem minmax(6rem, 11rem) 1fr auto;
    gap: 0.6rem;
    align-items: center;
    padding: 0 0.75rem 0 0.5rem;
    border-bottom: 1px solid color-mix(in srgb, var(--line) 55%, transparent);
    color: var(--fg-dim);
    cursor: default;
    position: relative;
  }
  .row > span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .row:hover {
    background: color-mix(in srgb, var(--sel-bg) 55%, transparent);
  }
  .dot {
    width: 0.45rem;
    height: 0.45rem;
    border-radius: 50%;
  }
  .unread {
    color: var(--fg);
    font-weight: 600;
  }
  .unread .dot {
    background: var(--accent);
  }
  .open {
    background: var(--accent-soft);
  }
  .cursor {
    background: var(--sel-bg);
  }
  .cursor::before {
    content: "";
    position: absolute;
    left: 0;
    top: 0;
    bottom: 0;
    width: 2px;
    background: var(--accent);
  }
  .pane:not(.focused) .cursor::before {
    background: var(--fg-dim);
  }
  .date {
    font-size: var(--fs-small);
    font-weight: 400;
    color: var(--fg-dim);
  }
  .state {
    position: absolute;
    inset: 0;
    display: grid;
    place-content: center;
    justify-items: center;
    gap: 0.5rem;
    padding: 1rem;
    text-align: center;
    color: var(--fg-dim);
    pointer-events: none;
  }
  .state button {
    pointer-events: auto;
  }
  .err {
    color: var(--danger);
  }
  .dim {
    font-size: var(--fs-small);
  }
  .more {
    margin: 0;
    padding: 0.2rem 0.75rem;
    font-size: var(--fs-small);
    color: var(--fg-dim);
    background: var(--bg-pane);
    border-top: 1px solid var(--line);
  }
</style>
