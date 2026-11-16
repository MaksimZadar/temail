<script lang="ts">
  import { tick } from "svelte";
  import { mail } from "../stores/mail.svelte";
  import { layout } from "../stores/layout.svelte";
  import { ui } from "../stores/ui.svelte";
  import { sync } from "../stores/sync.svelte";
  import { sel } from "../stores/select.svelte";
  import { actions, isStarred } from "../stores/actions.svelte";
  import type { ActionType, MessageHeader } from "../api";
  import { formatListDate, isUnread, keyOf, senderName } from "../mail/format";
  import { scrollTopFor, windowFor } from "../mail/virtual";

  const ROW = 28;

  let box = $state<HTMLDivElement>();
  let scrollTop = $state(0);
  let viewport = $state(400);

  const win = $derived(windowFor(scrollTop, viewport, ROW, mail.shown.length));
  const rows = $derived(mail.shown.slice(win.start, win.end));
  const focused = $derived(layout.focus === "list");
  // Compact label chips only when the list is wide enough to stay dense.
  const showLabels = $derived(
    (layout.narrow ? layout.width : layout.listW) >= 520,
  );
  const labelsOf = (m: {
    mailboxes?: { name: string; displayName: string; kind: string }[] | null;
  }) =>
    (m.mailboxes ?? []).filter(
      (r) => r.kind === "label" && r.name !== mail.current,
    );
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

  function click(e: MouseEvent, i: number): void {
    const k = keyOf(mail.shown[i]);
    if (e.ctrlKey || e.metaKey) return sel.toggleKey(k);
    if (e.shiftKey) return sel.extendTo(k);
    if (layout.narrow) layout.setFocus("view");
    void mail.open(mail.shown[i]);
  }

  // A row button acts on the selection if the row is in it, else on that row.
  function rowAct(e: Event, m: MessageHeader, type: ActionType): void {
    e.stopPropagation();
    const headers = sel.has(keyOf(m)) ? actions.targets() : [m];
    if (type === "mark_read" || type === "mark_unread")
      type = headers.every((h) => !isUnread(h)) ? "mark_unread" : "mark_read";
    if (type === "star" || type === "unstar")
      type = headers.every(isStarred) ? "unstar" : "star";
    void actions.run(type, {}, { headers });
  }

  // Right-click menu.
  let menu = $state<{ x: number; y: number; m: MessageHeader } | null>(null);
  function context(e: MouseEvent, i: number): void {
    e.preventDefault();
    const m = mail.shown[i];
    mail.cursorKey = keyOf(m);
    layout.focus = "list";
    menu = { x: e.clientX, y: e.clientY, m };
  }
  function menuAct(fn: () => void): void {
    menu = null;
    fn();
  }
  const menuTargets = (): MessageHeader[] =>
    menu && sel.has(keyOf(menu.m)) ? actions.targets() : menu ? [menu.m] : [];
  function menuRun(type: ActionType): void {
    const m = menu;
    if (!m) return;
    const headers = menuTargets();
    menuAct(() => void actions.run(type, {}, { headers }));
  }
  function menuPicker(kind: "move" | "label"): void {
    const headers = menuTargets();
    menuAct(() => {
      actions.pending = headers;
      ui.overlay = kind;
    });
  }
  $effect(() => {
    if (!menu) return;
    const close = (): void => {
      menu = null;
    };
    const key = (e: KeyboardEvent): void => {
      if (e.key === "Escape") {
        e.stopPropagation();
        close();
      }
    };
    window.addEventListener("click", close);
    window.addEventListener("blur", close);
    window.addEventListener("keydown", key, true);
    return () => {
      window.removeEventListener("click", close);
      window.removeEventListener("blur", close);
      window.removeEventListener("keydown", key, true);
    };
  });
</script>

<section
  class="pane"
  class:focused
  data-focused={focused}
  data-pane="list"
  aria-label="Messages"
  onfocusin={() => (layout.focus = "list")}
  onpointerdown={() => (layout.focus = "list")}
>
  <div
    class="scroller"
    role="listbox"
    aria-label="Messages in {mail.folder?.displayName ?? 'folder'}"
    aria-activedescendant={activeId}
    aria-busy={mail.loading}
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
            class:withLabels={showLabels}
            class:cursor={i === mail.cursorIndex}
            class:open={keyOf(m) === mail.openKey}
            class:selected={sel.has(keyOf(m))}
            class:busy={actions.busy[keyOf(m)]}
            role="option"
            tabindex="-1"
            aria-selected={sel.count > 0
              ? sel.has(keyOf(m))
              : i === mail.cursorIndex}
            aria-busy={actions.busy[keyOf(m)] ?? false}
            aria-posinset={i + 1}
            aria-setsize={mail.shown.length}
            aria-label="{unread ? 'Unread. ' : ''}{senderName(
              m.from,
            )}. {m.subject || '(no subject)'}. {formatListDate(m.date)}"
            style:height="{ROW}px"
            onclick={(e) => click(e, i)}
            oncontextmenu={(e) => context(e, i)}
          >
            <span class="dot" aria-hidden="true">
              {#if actions.busy[keyOf(m)]}<i class="spinner"
                ></i>{:else if sel.has(keyOf(m))}<svg viewBox="0 0 12 12"
                  ><path d="m2 6.5 2.6 2.6L10 3.4" /></svg
                >{/if}
            </span>
            <span class="from">{senderName(m.from)}</span>
            <span class="subject">{m.subject || "(no subject)"}</span>
            {#if showLabels}
              {@const ls = labelsOf(m)}
              <span class="labels" aria-hidden="true">
                {#each ls.slice(0, 2) as l (l.name)}
                  <span class="lab" title={l.displayName}
                    ><i></i>{l.displayName}</span
                  >
                {/each}
                {#if ls.length > 2}<span class="lab more">+{ls.length - 2}</span
                  >{/if}
              </span>
            {/if}
            <span class="date">{formatListDate(m.date)}</span>
            <span class="hover" role="group" aria-label="Quick actions">
              <button
                type="button"
                title="Archive (e)"
                aria-label="Archive"
                onclick={(e) => rowAct(e, m, "archive")}
              >
                <svg viewBox="0 0 16 16"
                  ><path d="M2 4h12v3H2zM3 7v6h10V7M6.5 9.5h3" /></svg
                >
              </button>
              <button
                type="button"
                title="Move to Trash (d)"
                aria-label="Move to Trash"
                onclick={(e) => rowAct(e, m, "trash")}
              >
                <svg viewBox="0 0 16 16"
                  ><path
                    d="M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8.5h5.8l.6-8.5"
                  /></svg
                >
              </button>
              <button
                type="button"
                title={unread ? "Mark as read (u)" : "Mark as unread (u)"}
                aria-label={unread ? "Mark as read" : "Mark as unread"}
                onclick={(e) => rowAct(e, m, "mark_read")}
              >
                <svg viewBox="0 0 16 16"
                  ><path d="M2 4h12v8H2zM2.5 4.5 8 9l5.5-4.5" /></svg
                >
              </button>
              <button
                type="button"
                title={isStarred(m) ? "Unstar (s)" : "Star (s)"}
                aria-label={isStarred(m) ? "Unstar" : "Star"}
                onclick={(e) => rowAct(e, m, "star")}
              >
                <svg viewBox="0 0 16 16" class:on={isStarred(m)}
                  ><path
                    d="m8 2 1.8 3.8 4.2.5-3.1 2.9.8 4.1L8 11.2 4.3 13.3l.8-4.1L2 6.3l4.2-.5z"
                  /></svg
                >
              </button>
            </span>
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
  {#if menu}
    <div
      class="menu"
      role="menu"
      aria-label="Message actions"
      style:left="{Math.min(menu.x, window.innerWidth - 200)}px"
      style:top="{Math.min(menu.y, window.innerHeight - 290)}px"
    >
      <button role="menuitem" type="button" onclick={() => menuRun("archive")}
        >Archive <kbd>e</kbd></button
      >
      <button role="menuitem" type="button" onclick={() => menuRun("trash")}
        >Move to Trash <kbd>d</kbd></button
      >
      <button role="menuitem" type="button" onclick={() => menuRun("spam")}
        >Mark as spam <kbd>!</kbd></button
      >
      <button role="menuitem" type="button" onclick={() => menuPicker("move")}
        >Move to… <kbd>m</kbd></button
      >
      <button role="menuitem" type="button" onclick={() => menuPicker("label")}
        >Labels… <kbd>;</kbd></button
      >
      <hr />
      <button role="menuitem" type="button" onclick={() => menuRun("mark_read")}
        >Mark as read</button
      >
      <button
        role="menuitem"
        type="button"
        onclick={() => menuRun("mark_unread")}>Mark as unread</button
      >
      <button role="menuitem" type="button" onclick={() => menuRun("star")}
        >Star <kbd>s</kbd></button
      >
      <button role="menuitem" type="button" onclick={() => menuRun("unstar")}
        >Unstar</button
      >
    </div>
  {/if}
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
    border: 1px solid var(--line);
    border-radius: 8px;
    overflow: hidden;
    transition:
      border-color 0.12s,
      box-shadow 0.12s;
  }
  .pane.focused {
    border-color: var(--accent);
    box-shadow:
      0 0 0 1px var(--accent),
      0 0 0 4px var(--accent-soft);
  }
  @media (forced-colors: active) {
    .pane.focused {
      border-color: Highlight;
      outline: 2px solid Highlight;
      outline-offset: -1px;
    }
  }
  .scroller {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    position: relative;
  }
  /* The pane border is the focus indicator; no second ring inside it. */
  .scroller:focus-visible {
    outline: none;
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
  .row.withLabels {
    grid-template-columns: 0.5rem minmax(6rem, 11rem) 1fr auto auto;
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
  .row.selected {
    background: color-mix(in srgb, var(--accent) 22%, transparent);
  }
  .row.selected::before {
    content: "";
    position: absolute;
    left: 0;
    top: 0;
    bottom: 0;
    width: 2px;
    background: var(--accent);
  }
  .row.busy {
    opacity: 0.6;
  }
  .row.selected .dot {
    display: grid;
    place-items: center;
    width: 0.9rem;
    height: 0.9rem;
    margin-left: -0.2rem;
    border-radius: 3px;
    background: var(--accent);
  }
  .dot svg {
    width: 0.7rem;
    height: 0.7rem;
    fill: none;
    stroke: var(--accent-fg);
    stroke-width: 2;
  }
  .spinner {
    display: block;
    width: 0.7rem;
    height: 0.7rem;
    margin: -0.12rem;
    border: 2px solid var(--line);
    border-top-color: var(--accent);
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .spinner {
      animation: none;
    }
  }
  /* Quick actions replace the date on hover, and on the cursor row. */
  .row > .hover {
    display: none;
    position: absolute;
    right: 0.35rem;
    top: 50%;
    transform: translateY(-50%);
    gap: 0.1rem;
    padding: 0 0.15rem;
    overflow: visible;
    background: var(--bg-raised);
    border: 1px solid var(--line);
    border-radius: 6px;
  }
  .row:hover > .hover,
  .row:focus-within > .hover,
  .row.cursor > .hover {
    display: inline-flex;
  }
  .hover button {
    display: grid;
    place-items: center;
    width: 1.35rem;
    height: 1.35rem;
    background: none;
    border: 0;
    border-radius: 4px;
    color: var(--fg-dim);
  }
  .hover button:hover {
    background: var(--sel-bg);
    color: var(--fg);
  }
  .hover svg {
    width: 0.9rem;
    height: 0.9rem;
    fill: none;
    stroke: currentColor;
    stroke-width: 1.4;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .hover svg.on {
    fill: var(--accent);
    stroke: var(--accent);
  }
  .menu {
    position: fixed;
    z-index: 15;
    min-width: 11.5rem;
    padding: 0.25rem;
    background: var(--bg-raised);
    border: 1px solid var(--line);
    border-radius: 8px;
    box-shadow: 0 8px 28px #0005;
  }
  .menu button {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
    width: 100%;
    padding: 0.3rem 0.6rem;
    text-align: left;
    background: none;
    border: 0;
    border-radius: 5px;
  }
  .menu button:hover,
  .menu button:focus-visible {
    background: var(--sel-bg);
  }
  .menu kbd {
    color: var(--fg-dim);
  }
  .menu hr {
    margin: 0.25rem 0;
    border: 0;
    border-top: 1px solid var(--line);
  }
  .labels {
    display: inline-flex;
    gap: 0.25rem;
    min-width: 0;
  }
  .lab {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    max-width: 6.5rem;
    padding: 0 0.4rem;
    height: 1.05rem;
    border-radius: 999px;
    background: var(--accent-soft);
    color: var(--fg);
    font-size: 0.6875rem;
    font-weight: 400;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .lab i {
    flex: none;
    width: 0.3rem;
    height: 0.3rem;
    border-radius: 50%;
    background: var(--accent);
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
