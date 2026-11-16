<script lang="ts">
  import { onMount, tick } from "svelte";
  import { suggest } from "../stores/suggest.svelte";
  import { ui } from "../stores/ui.svelte";
  import { trapFocus } from "../focus";
  import { senderName } from "../mail/format";
  import {
    clip,
    codeText,
    confidenceText,
    elapsed,
    plural,
  } from "../suggest/text";

  let card: HTMLDivElement;
  let list = $state<HTMLElement>();
  let opener: Element | null = null;

  const s = suggest;
  const t = $derived(s.current);
  const r = $derived(s.currentResult);
  const labels = $derived(r?.ok ? (r.labels ?? []) : []);
  const ticked = $derived(s.tickedCount);
  const level = (c: number): number => (c >= 0.8 ? 3 : c >= 0.5 ? 2 : 1);
  const providerName = $derived(
    s.provider ? s.provider.name : "your AI provider",
  );
  const progress = $derived(
    s.phase === "failed"
      ? ""
      : s.running
        ? `${s.analysed} of ${s.total} analysed`
        : s.done
          ? s.done.failed > 0
            ? `${s.analysed - s.done.failed} of ${s.total} analysed · ${s.done.failed} failed`
            : `${s.total} of ${s.total} analysed`
          : `${s.analysed} of ${s.total} analysed`,
  );
  const privacy = $derived(
    `${s.total === 1 ? "This email's content is" : "These emails' content is"} sent to ${providerName}.`,
  );
  const pct = $derived(
    s.total > 0 ? Math.round((s.analysed / s.total) * 100) : 0,
  );

  onMount(() => {
    opener = document.activeElement;
    card.focus();
    window.addEventListener("keydown", onKey, true);
    return () => {
      window.removeEventListener("keydown", onKey, true);
      (opener as HTMLElement | null)?.focus?.();
    };
  });

  // Once there is a list to move in, focus it (screen readers follow it).
  $effect(() => {
    if (list && document.activeElement === card) list.focus();
  });

  const cancel = (): void => {
    if (!s.applying) s.close();
  };

  function goPage(d: number): void {
    s.goPage(d);
    void tick().then(() => list?.focus());
  }

  // Keys inside the popup. A capture listener keeps the mailbox keys (e, d, j…)
  // from reaching the app behind it. Only ? passes through (help).
  function onKey(e: KeyboardEvent): void {
    if (!s.open || ui.overlay !== "none") return;
    if (e.isComposing || e.keyCode === 229) return;
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    const k = e.key;
    if (
      k === "?" ||
      k === "Tab" ||
      (k.length > 1 &&
        !k.startsWith("Arrow") &&
        k !== "Enter" &&
        k !== "Escape")
    )
      return;
    const onButton = (e.target as HTMLElement | null)?.tagName === "BUTTON";
    if ((k === "Enter" || k === " ") && onButton) {
      e.stopPropagation(); // native click happens; the app behind stays out
      return;
    }
    e.preventDefault();
    e.stopImmediatePropagation();
    switch (k) {
      case "Escape":
        return cancel();
      case "j":
      case "ArrowDown":
        return s.moveCursor(1);
      case "k":
      case "ArrowUp":
        return s.moveCursor(-1);
      case " ":
      case "Enter":
        return s.toggleCursor();
      case "h":
      case "ArrowLeft":
        return goPage(-1);
      case "l":
      case "ArrowRight":
        return goPage(1);
      case "a":
        return void s.accept();
    }
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<div class="scrim" role="presentation" onclick={cancel}>
  <div
    class="card"
    role="dialog"
    aria-modal="true"
    aria-labelledby="sg-title"
    aria-describedby="sg-privacy"
    tabindex="-1"
    bind:this={card}
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
  >
    <header>
      <div class="titles">
        <h2 id="sg-title">Suggest labels</h2>
        {#if s.provider}
          <p class="prov">
            {s.provider.name}{s.provider.model ? ` · ${s.provider.model}` : ""}
            <span class="badge" class:cloud={s.cloud}
              >{s.cloud ? "Cloud" : "Local"}</span
            >
          </p>
        {/if}
      </div>
      <button
        class="x"
        type="button"
        aria-label="Cancel and discard"
        title="Cancel (Esc)"
        disabled={s.applying}
        onclick={cancel}>×</button
      >
    </header>

    <p id="sg-privacy" class="privacy" class:show={s.cloud}>
      {#if s.cloud}
        {privacy}
      {:else}
        Runs on this computer. Nothing leaves it.
      {/if}
    </p>

    <div class="progress">
      <span class="sr-only" role="status" aria-live="polite">{progress}</span>
      <span aria-hidden="true">{progress}</span>
      {#if s.running}
        <i class="bar"><b style:width="{pct}%"></b></i>
      {/if}
    </div>

    {#if s.fatal}
      <div class="body">
        <p class="err" role="alert">{s.fatal.text}</p>
        {#if s.fatal.code !== "error"}
          <button
            class="btn"
            type="button"
            onclick={() => {
              s.close();
              ui.screen = "settings";
            }}>Open Settings</button
          >
        {/if}
      </div>
    {:else if t}
      <div class="body">
        <div class="mail">
          <p class="subj" title={t.header.subject}>
            {t.header.subject || "(no subject)"}
          </p>
          <p class="from">{senderName(t.header.from)}</p>
        </div>

        {#if s.applied[t.mid]}
          <p class="note ok" role="status">Labels applied to this email.</p>
        {:else if !r}
          <p class="note" role="status">
            {#if s.running}<i class="spinner" aria-hidden="true"></i>Analysing…
              <span class="clock">{elapsed(s.elapsed)}</span>
              {#if !s.cloud}<span class="hint"
                  >Local models can take a while. You can keep waiting or
                  cancel.</span
                >{/if}
            {:else}No result for this email.{/if}
          </p>
        {:else if !r.ok}
          <p class="err" role="alert">
            {codeText(r.code, r.error)}
            {#if r.code}<span class="code">({r.code})</span>{/if}
          </p>
        {:else}
          {#if r.summary}<p class="summary">{r.summary}</p>{/if}
          <div class="bulk">
            <button
              class="link"
              type="button"
              onclick={() => s.setAll(t.mid, true)}>Accept all</button
            >
            <button
              class="link"
              type="button"
              onclick={() => s.setAll(t.mid, false)}>Reject all</button
            >
          </div>
          {#if labels.length === 0}
            <p class="note">No labels suggested for this email.</p>
          {:else}
            <ul
              class="labels"
              role="listbox"
              aria-label="Suggested labels for {t.header.subject ||
                'this email'}"
              aria-multiselectable="true"
              aria-activedescendant="sg-l{s.cursor}"
              tabindex="0"
              bind:this={list}
            >
              {#each labels as l, i (l.name)}
                {@const on = s.isTicked(t.mid, l.name)}
                <!-- svelte-ignore a11y_click_events_have_key_events -->
                <li
                  id="sg-l{i}"
                  role="option"
                  aria-selected={on}
                  class:active={i === s.cursor}
                  onclick={() => {
                    s.cursor = i;
                    s.toggle(t.mid, l.name);
                  }}
                  onmousemove={() => (s.cursor = i)}
                >
                  <span class="box" class:on aria-hidden="true"
                    >{#if on}✓{/if}</span
                  >
                  <span class="main">
                    <span class="line">
                      <span class="name">{l.name}</span>
                      {#if l.target === "folder"}<span
                          class="chip folder"
                          title="A folder with this name exists. Accepting moves the message to it (undo available)."
                          ><svg
                            viewBox="0 0 16 16"
                            width="12"
                            height="12"
                            aria-hidden="true"
                            ><path
                              d="M1.5 3.5h4l1.5 1.5h7.5v7.5h-13z"
                              fill="none"
                              stroke="currentColor"
                              stroke-width="1.3"
                              stroke-linejoin="round"
                            /></svg
                          >move to folder</span
                        >{:else if l.target === "new" || (!l.target && !l.existing)}<span
                          class="new"
                          title="Creates a new label on Proton (cannot be undone)"
                          >new</span
                        >{:else}<span
                          class="chip"
                          title="Adds this existing label to the message"
                          >add label</span
                        >{/if}
                      <span
                        class="conf"
                        title={confidenceText(l.confidence)}
                        aria-hidden="true"
                      >
                        {#each [1, 2, 3] as n (n)}<i
                            class:fill={n <= level(l.confidence)}
                          ></i>{/each}
                      </span>
                      <span class="sr-only">{confidenceText(l.confidence)}</span
                      >
                    </span>
                    {#if l.reason}<span class="reason">{l.reason}</span>{/if}
                  </span>
                </li>
              {/each}
            </ul>
          {/if}
        {/if}

        {#if s.applyError}
          <p class="err" role="alert">{s.applyError}</p>
        {/if}
        {#if s.applyFailures.length > 0}
          <div class="fails" role="alert">
            <p>Some emails couldn't be labelled:</p>
            <ul>
              {#each s.applyFailures as f (f.mid)}
                <li>
                  <b>{s.subjectOf(f.mid)}</b>: {f.text}
                  {#if f.detail}<span class="detail" title={f.detail}
                      >{clip(f.detail)}</span
                    >{/if}
                </li>
              {/each}
            </ul>
          </div>
        {/if}
      </div>
    {/if}

    <footer>
      <div class="pager">
        {#if s.total > 1}
          <button
            class="ib"
            type="button"
            aria-label="Previous email"
            title="Previous email (h)"
            disabled={s.page === 0}
            onclick={() => goPage(-1)}>‹</button
          >
        {/if}
        <span class="page" aria-live="polite"
          >{s.page + 1} of {Math.max(1, s.total)}</span
        >
        {#if s.total > 1}
          <button
            class="ib"
            type="button"
            aria-label="Next email"
            title="Next email (l)"
            disabled={s.page >= s.total - 1}
            onclick={() => goPage(1)}>›</button
          >
        {/if}
      </div>
      <div class="btns">
        <button
          class="btn"
          type="button"
          disabled={s.applying}
          title="Cancel and discard (Esc)"
          onclick={cancel}>Cancel <kbd>Esc</kbd></button
        >
        <button
          class="btn primary"
          type="button"
          disabled={s.applying || ticked === 0 || !!s.fatal}
          title="Apply the ticked labels (a)"
          onclick={() => void s.accept()}
          >{s.applying
            ? "Applying…"
            : ticked > 0
              ? `Accept ${plural(ticked, "label")}`
              : "Accept"} <kbd>a</kbd></button
        >
      </div>
    </footer>
    <p class="keys">
      <kbd>j</kbd><kbd>k</kbd> move · <kbd>Space</kbd> tick · <kbd>h</kbd><kbd
        >l</kbd
      >
      emails · <kbd>a</kbd> accept · <kbd>Esc</kbd> cancel
    </p>
  </div>
</div>

<style>
  .scrim {
    position: fixed;
    inset: 0;
    display: flex;
    justify-content: center;
    padding-top: 9vh;
    background: var(--scrim);
    z-index: 10;
  }
  .card {
    width: min(35rem, 94vw);
    max-height: 82vh;
    align-self: flex-start;
    display: flex;
    flex-direction: column;
    background: var(--bg-raised);
    border: 1px solid var(--line);
    border-radius: 10px;
    box-shadow: 0 12px 40px #0006;
    overflow: hidden;
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 1rem;
    padding: 0.8rem 1rem 0.2rem;
  }
  h2 {
    font-size: 1.05rem;
  }
  .prov {
    margin: 0.15rem 0 0;
    font-size: var(--fs-small);
    color: var(--fg-dim);
  }
  .badge {
    margin-left: 0.3rem;
    padding: 0 0.4rem;
    border: 1px solid var(--line);
    border-radius: 999px;
    font-size: 0.7rem;
  }
  .badge.cloud {
    color: var(--warn);
    border-color: var(--warn);
  }
  .x {
    background: none;
    border: 0;
    color: var(--fg-dim);
    font-size: 1.3rem;
    line-height: 1;
    padding: 0.1rem 0.45rem;
    border-radius: 5px;
  }
  .x:hover:not(:disabled) {
    background: var(--sel-bg);
  }
  .privacy {
    margin: 0.3rem 1rem 0;
    font-size: var(--fs-small);
    color: var(--fg-dim);
  }
  .privacy.show {
    padding: 0.35rem 0.6rem;
    color: var(--fg);
    background: var(--accent-soft);
    border-left: 3px solid var(--accent);
    border-radius: 4px;
  }
  .progress {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    margin: 0.5rem 1rem 0;
    font-size: var(--fs-small);
    color: var(--fg-dim);
    font-variant-numeric: tabular-nums;
  }
  .bar {
    flex: 1;
    height: 3px;
    background: var(--line);
    border-radius: 2px;
    overflow: hidden;
  }
  .bar b {
    display: block;
    height: 100%;
    background: var(--accent);
    transition: width 0.2s;
  }
  .body {
    flex: 1;
    min-height: 9rem;
    overflow: auto;
    padding: 0.7rem 1rem;
    display: grid;
    align-content: start;
    gap: 0.5rem;
  }
  .mail {
    padding-bottom: 0.4rem;
    border-bottom: 1px solid var(--line);
  }
  .subj {
    margin: 0;
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .from {
    margin: 0;
    font-size: var(--fs-small);
    color: var(--fg-dim);
  }
  .summary {
    margin: 0;
    color: var(--fg-dim);
    font-size: var(--fs-small);
    font-style: italic;
  }
  .bulk {
    display: flex;
    gap: 0.9rem;
  }
  .link {
    background: none;
    border: 0;
    padding: 0.1rem 0.2rem;
    color: var(--accent);
    font-size: var(--fs-small);
    border-radius: 4px;
  }
  .link:hover {
    text-decoration: underline;
  }
  .labels {
    margin: 0;
    padding: 0.2rem;
    list-style: none;
    border: 1px solid var(--line);
    border-radius: 8px;
  }
  .labels:focus-visible {
    outline-offset: 2px;
  }
  .labels li {
    display: flex;
    gap: 0.6rem;
    padding: 0.45rem 0.55rem;
    border-radius: 6px;
    cursor: pointer;
  }
  .labels li.active {
    background: var(--sel-bg);
  }
  .box {
    flex: none;
    display: inline-grid;
    place-items: center;
    width: 1.05rem;
    height: 1.05rem;
    margin-top: 0.1rem;
    border: 1px solid var(--fg-dim);
    border-radius: 4px;
    font-size: 0.75rem;
    line-height: 1;
  }
  .box.on {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--accent-fg);
  }
  .main {
    display: grid;
    gap: 0.1rem;
    min-width: 0;
  }
  .line {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .name {
    font-weight: 600;
  }
  .new {
    padding: 0 0.4rem;
    font-size: 0.7rem;
    color: var(--accent);
    background: var(--accent-soft);
    border-radius: 999px;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    padding: 0 0.4rem;
    font-size: 0.7rem;
    color: var(--fg-dim);
    border: 1px solid var(--line);
    border-radius: 999px;
  }
  .chip.folder {
    color: var(--accent);
    border-color: var(--accent);
  }
  .clock {
    font-variant-numeric: tabular-nums;
  }
  .hint {
    font-size: var(--fs-small);
  }
  .detail {
    display: block;
    color: var(--fg-dim);
    overflow-wrap: anywhere;
  }
  .conf {
    display: inline-flex;
    gap: 2px;
    margin-left: auto;
  }
  .conf i {
    width: 0.3rem;
    height: 0.7rem;
    border-radius: 1px;
    background: var(--line);
  }
  .conf i.fill {
    background: var(--accent);
  }
  .reason {
    font-size: var(--fs-small);
    color: var(--fg-dim);
  }
  .note {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin: 0;
    color: var(--fg-dim);
  }
  .note.ok {
    color: var(--ok);
  }
  .err {
    margin: 0;
    color: var(--danger);
  }
  .code {
    font-size: var(--fs-small);
    opacity: 0.8;
  }
  .fails {
    padding: 0.5rem 0.7rem;
    border: 1px solid var(--danger);
    border-radius: 6px;
    font-size: var(--fs-small);
  }
  .fails p {
    margin: 0 0 0.2rem;
  }
  .fails ul {
    margin: 0;
    padding-left: 1.1rem;
  }
  footer {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 0.55rem 1rem;
    border-top: 1px solid var(--line);
  }
  .pager,
  .btns {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .page {
    min-width: 3.5rem;
    text-align: center;
    color: var(--fg-dim);
    font-variant-numeric: tabular-nums;
  }
  .ib {
    width: 1.8rem;
    height: 1.8rem;
    background: none;
    border: 1px solid var(--line);
    border-radius: 6px;
    font-size: 1.1rem;
    line-height: 1;
  }
  .ib:hover:not(:disabled) {
    border-color: var(--accent);
  }
  .ib:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
  .keys {
    margin: 0;
    padding: 0 1rem 0.6rem;
    font-size: 0.72rem;
    color: var(--fg-dim);
  }
  .spinner {
    width: 0.8rem;
    height: 0.8rem;
    border: 2px solid var(--line);
    border-top-color: var(--accent);
    border-radius: 50%;
  }
  @media (prefers-reduced-motion: no-preference) {
    .spinner {
      animation: spin 0.8s linear infinite;
    }
    @keyframes spin {
      to {
        transform: rotate(360deg);
      }
    }
  }
</style>
