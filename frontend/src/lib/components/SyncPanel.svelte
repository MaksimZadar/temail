<script lang="ts">
  import { onMount } from "svelte";
  import { ui } from "../stores/ui.svelte";
  import { sync } from "../stores/sync.svelte";
  import { trapFocus } from "../focus";
  import SyncList from "./SyncList.svelte";

  let panel: HTMLDivElement;
  let opener: Element | null = null;

  onMount(() => {
    opener = document.activeElement;
    panel.focus();
    return () => (opener as HTMLElement | null)?.focus?.();
  });
</script>

<!-- Click outside closes. Esc is handled by the key dispatcher. -->
<!-- svelte-ignore a11y_click_events_have_key_events -->
<div class="scrim" role="presentation" onclick={() => (ui.overlay = "none")}>
  <div
    class="panel"
    role="dialog"
    aria-label="Sync status"
    tabindex="-1"
    bind:this={panel}
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
  >
    <header>
      <h2>{sync.text}</h2>
      <button class="btn" type="button" onclick={() => (ui.overlay = "none")}
        >Close</button
      >
    </header>
    {#if sync.messages > 0}
      <p class="dim">{sync.messages.toLocaleString()} messages cached</p>
    {/if}
    <div class="body"><SyncList /></div>
  </div>
</div>

<style>
  .scrim {
    position: fixed;
    inset: 0;
    z-index: 10;
  }
  .panel {
    position: absolute;
    left: 0.75rem;
    bottom: 2.25rem;
    width: min(26rem, 94vw);
    max-height: 70vh;
    display: flex;
    flex-direction: column;
    padding: 0.75rem 1rem;
    background: var(--bg-raised);
    border: 1px solid var(--line);
    border-radius: 10px;
    box-shadow: 0 8px 30px #0005;
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 1rem;
  }
  h2 {
    font-size: var(--fs-body);
  }
  .dim {
    color: var(--fg-dim);
    margin-bottom: 0.25rem;
  }
  .body {
    overflow: auto;
    margin-top: 0.5rem;
  }
</style>
