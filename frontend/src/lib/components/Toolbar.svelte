<script lang="ts">
  import { mail } from "../stores/mail.svelte";
  import { layout } from "../stores/layout.svelte";
  import { sync } from "../stores/sync.svelte";
  import { ui } from "../stores/ui.svelte";

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
