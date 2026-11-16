<script lang="ts">
  import { mail } from "../stores/mail.svelte";
  import { layout } from "../stores/layout.svelte";
  import { sync } from "../stores/sync.svelte";
  import { ui } from "../stores/ui.svelte";
  import { sel } from "../stores/select.svelte";
  import { actions } from "../stores/actions.svelte";
  import { suggest } from "../stores/suggest.svelte";
  import type { ActionType } from "../api";

  const acts: { type: ActionType; label: string; key: string; d: string }[] = [
    {
      type: "archive",
      label: "Archive",
      key: "e",
      d: "M2 4h12v3H2zM3 7v6h10V7M6.5 9.5h3",
    },
    {
      type: "trash",
      label: "Move to Trash",
      key: "d",
      d: "M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8.5h5.8l.6-8.5",
    },
    {
      type: "spam",
      label: "Mark as spam",
      key: "!",
      d: "M8 2 14 13H2zM8 6.5v3M8 11.2v.1",
    },
  ];

  let { filterInput = $bindable() }: { filterInput?: HTMLInputElement } =
    $props();

  function refresh(): void {
    void sync.syncNow();
  }
  function key(e: KeyboardEvent): void {
    if (e.key === "Escape") {
      mail.setFilter("");
      (e.target as HTMLElement).blur();
    } else if (e.key === "Enter" || e.key === "ArrowDown") {
      (e.target as HTMLElement).blur();
      layout.setFocus("list");
    } else return;
    e.preventDefault();
  }
</script>

<div class="toolbar" role="toolbar" aria-label="Mailbox">
  {#if layout.narrow && layout.focus !== "folders"}
    <button
      class="ib"
      type="button"
      title="Back (h)"
      aria-label="Back"
      onclick={() => layout.step(-1)}
    >
      <svg viewBox="0 0 16 16" aria-hidden="true"
        ><path d="M10 3 5 8l5 5" /></svg
      >
    </button>
  {/if}
  <button
    class="ib"
    type="button"
    title="Show or hide folders (b)"
    aria-label="Show or hide folders"
    aria-pressed={layout.narrow
      ? layout.focus === "folders"
      : layout.folderVisible}
    onclick={() => layout.toggleFolders()}
  >
    <svg viewBox="0 0 16 16" aria-hidden="true"
      ><path d="M2 4h12M2 8h12M2 12h12" /></svg
    >
  </button>
  <h1 class="title">{mail.folder?.displayName ?? "temail"}</h1>
  <label class="filter">
    <span class="sr-only">Filter messages by subject or sender</span>
    <svg viewBox="0 0 16 16" aria-hidden="true"
      ><circle cx="7" cy="7" r="4.5" /><path d="m10.5 10.5 3.5 3.5" /></svg
    >
    <input
      bind:this={filterInput}
      type="search"
      placeholder="Filter this folder"
      title="Filter by subject or sender (/)"
      autocomplete="off"
      spellcheck="false"
      value={mail.filter}
      oninput={(e) => mail.setFilter(e.currentTarget.value)}
      onkeydown={key}
    />
  </label>
  <span class="grow"></span>
  {#if sel.count > 0}
    <span class="selcount" role="status"
      >{sel.visual ? "Selecting · " : ""}{sel.count} selected
      <button
        type="button"
        title="Clear selection (Esc)"
        aria-label="Clear selection"
        onclick={() => sel.clear()}>×</button
      ></span
    >
  {/if}
  <div class="acts" role="group" aria-label="Message actions">
    {#each acts as a (a.type)}
      <button
        class="ib"
        type="button"
        title="{a.label} ({a.key})"
        aria-label={a.label}
        onclick={() => void actions.run(a.type)}
      >
        <svg viewBox="0 0 16 16" aria-hidden="true"><path d={a.d} /></svg>
      </button>
    {/each}
    <button
      class="ib"
      type="button"
      title="Move to… (m)"
      aria-label="Move to"
      onclick={() => actions.openPicker("move")}
    >
      <svg viewBox="0 0 16 16" aria-hidden="true"
        ><path
          d="M2 4.5h4l1.2 1.5H14v6.5H2zM8 9h4M10.5 7.5 12 9l-1.5 1.5"
        /></svg
      >
    </button>
    <button
      class="ib"
      type="button"
      title="Suggest labels (S)"
      aria-label="Suggest labels"
      onclick={() => suggest.trigger()}
    >
      <svg viewBox="0 0 16 16" aria-hidden="true"
        ><path
          d="M6.5 2.5 7.6 5.4l2.9 1.1-2.9 1.1-1.1 2.9-1.1-2.9-2.9-1.1 2.9-1.1zM12 9l.6 1.6 1.6.6-1.6.6L12 13.4l-.6-1.6-1.6-.6 1.6-.6z"
        /></svg
      >
    </button>
    <button
      class="ib"
      type="button"
      title="Labels (;)"
      aria-label="Labels"
      onclick={() => actions.openPicker("label")}
    >
      <svg viewBox="0 0 16 16" aria-hidden="true"
        ><path d="M2 8.5V3h5.5L14 9.5 9.5 14zM5 5.5v.1" /></svg
      >
    </button>
    <button
      class="ib"
      type="button"
      title="Mark read or unread (u)"
      aria-label="Mark read or unread"
      onclick={() => void actions.toggleRead()}
    >
      <svg viewBox="0 0 16 16" aria-hidden="true"
        ><path d="M2 4h12v8H2zM2.5 4.5 8 9l5.5-4.5" /></svg
      >
    </button>
    <button
      class="ib"
      type="button"
      title="Star (s)"
      aria-label="Star"
      onclick={() => void actions.toggleStar()}
    >
      <svg viewBox="0 0 16 16" aria-hidden="true"
        ><path
          d="m8 2 1.8 3.8 4.2.5-3.1 2.9.8 4.1L8 11.2 4.3 13.3l.8-4.1L2 6.3l4.2-.5z"
        /></svg
      >
    </button>
    <button
      class="ib"
      type="button"
      title="Undo (z)"
      aria-label="Undo"
      onclick={() => void actions.undoLast()}
    >
      <svg viewBox="0 0 16 16" aria-hidden="true"
        ><path d="M5 3 2 6l3 3M2 6h7a4 4 0 0 1 0 8H6" /></svg
      >
    </button>
    <button
      class="ib"
      type="button"
      title="Recent activity (g u)"
      aria-label="Recent activity"
      onclick={() => (ui.overlay = "activity")}
    >
      <svg viewBox="0 0 16 16" aria-hidden="true"
        ><circle cx="8" cy="8" r="6" /><path d="M8 4.5V8l2.5 1.5" /></svg
      >
    </button>
  </div>
  <button
    class="ib"
    class:spin={sync.running}
    type="button"
    title="Sync now (g r)"
    aria-label="Sync now"
    onclick={refresh}
  >
    <svg viewBox="0 0 16 16" aria-hidden="true"
      ><path d="M13 8a5 5 0 1 1-1.5-3.5M13 2.5v3h-3" /></svg
    >
  </button>
  <button
    class="ib"
    type="button"
    title="Settings (g ,)"
    aria-label="Settings"
    onclick={() => (ui.screen = "settings")}
  >
    <svg viewBox="0 0 16 16" aria-hidden="true"
      ><circle cx="8" cy="8" r="2.2" /><path
        d="M8 1.8v1.6M8 12.6v1.6M1.8 8h1.6M12.6 8h1.6M3.6 3.6l1.1 1.1M11.3 11.3l1.1 1.1M3.6 12.4l1.1-1.1M11.3 4.7l1.1-1.1"
      /></svg
    >
  </button>
  <button
    class="ib"
    type="button"
    title="Commands (:)"
    aria-label="Commands"
    onclick={() => (ui.overlay = "palette")}
  >
    <svg viewBox="0 0 16 16" aria-hidden="true"
      ><path d="m3 5 4 3-4 3M8 12h5" /></svg
    >
  </button>
  <button
    class="ib"
    type="button"
    title="Keyboard shortcuts (?)"
    aria-label="Keyboard shortcuts"
    onclick={() => (ui.overlay = "help")}
  >
    <svg viewBox="0 0 16 16" aria-hidden="true"
      ><circle cx="8" cy="8" r="6" /><path
        d="M6.2 6.3a1.9 1.9 0 1 1 2.6 1.8c-.5.3-.8.6-.8 1.2M8 11.6v.1"
      /></svg
    >
  </button>
</div>

<style>
  .acts {
    display: flex;
    gap: 0.1rem;
    padding-right: 0.4rem;
    margin-right: 0.2rem;
    border-right: 1px solid var(--line);
  }
  .selcount {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0 0.2rem 0 0.6rem;
    height: 1.5rem;
    border-radius: 999px;
    background: var(--accent-soft);
    font-size: var(--fs-small);
    font-variant-numeric: tabular-nums;
  }
  .selcount button {
    background: none;
    border: 0;
    border-radius: 50%;
    color: var(--fg-dim);
  }
  .toolbar {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    height: 2.5rem;
    padding: 0 0.6rem;
    background: var(--bg-pane);
    border-bottom: 1px solid var(--line);
  }
  .title {
    margin: 0 0.5rem 0 0.25rem;
    font-size: var(--fs-body);
    min-width: 4rem;
    white-space: nowrap;
  }
  .grow {
    flex: 1;
  }
  .ib {
    display: inline-grid;
    place-items: center;
    width: 1.9rem;
    height: 1.9rem;
    padding: 0;
    background: none;
    border: 1px solid transparent;
    border-radius: 6px;
    color: var(--fg);
  }
  .ib:hover {
    background: var(--accent-soft);
  }
  .ib[aria-pressed="true"] {
    background: var(--sel-bg);
  }
  svg {
    width: 1rem;
    height: 1rem;
    fill: none;
    stroke: currentColor;
    stroke-width: 1.5;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .spin svg {
    animation: spin 1.2s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  .filter {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    width: min(20rem, 40%);
    padding: 0 0.6rem;
    background: var(--bg);
    border: 1px solid var(--line);
    border-radius: 6px;
    color: var(--fg-dim);
  }
  .filter:focus-within {
    border-color: var(--accent);
  }
  input {
    flex: 1;
    min-width: 0;
    height: 1.75rem;
    background: none;
    border: 0;
    color: var(--fg);
  }
  input:focus-visible {
    outline: none;
  }
</style>
