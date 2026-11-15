<script lang="ts">
  import {
    continuations,
    formatKeys,
    isReserved,
    type Binding,
  } from "../keys/keymap";
  import { ui } from "../stores/ui.svelte";

  const items = $derived(
    continuations(ui.pending, [ui.context, "global"]).filter(
      (b) => !isReserved(b),
    ),
  );
  const prefix = $derived(formatKeys(ui.pending));
  const visible = $derived(ui.whichKey && items.length > 0);

  // Said once when the prefix starts, not on every change.
  const spoken = $derived(
    ui.pending.length > 0 && items.length > 0
      ? `${prefix} pressed. Options: ${items
          .map(
            (b) => `${formatKeys(b.keys.slice(ui.pending.length))} ${b.desc}`,
          )
          .join(", ")}. Press Escape to cancel.`
      : "",
  );

  function pick(b: Binding): void {
    ui.triggerBinding(b);
  }
</script>

<span class="sr-only" role="status" aria-live="polite">{spoken}</span>

{#if visible}
  <div class="wk" role="group" aria-label="Keys after {prefix}">
    <div class="title">
      <kbd>{prefix}</kbd> then… <span class="esc">Esc cancels</span>
    </div>
    {#each items as b (b.action + b.keys.join())}
      <button type="button" class="entry" onclick={() => pick(b)}>
        <kbd>{formatKeys(b.keys.slice(ui.pending.length))}</kbd>
        <span>{b.desc}</span>
      </button>
    {/each}
  </div>
{/if}

<style>
  .wk {
    position: fixed;
    right: 0.75rem;
    bottom: 2.5rem;
    min-width: 13rem;
    padding: 0.4rem;
    background: var(--bg-raised);
    border: 1px solid var(--line);
    border-radius: 8px;
    box-shadow: 0 6px 24px #0004;
    display: grid;
    gap: 0.1rem;
    z-index: 9;
  }
  .title {
    padding: 0.2rem 0.4rem 0.3rem;
    color: var(--fg-dim);
    font-size: var(--fs-small);
  }
  .esc {
    float: right;
  }
  .entry {
    display: flex;
    gap: 0.6rem;
    align-items: center;
    width: 100%;
    padding: 0.25rem 0.4rem;
    text-align: left;
    background: none;
    border: 0;
    border-radius: 5px;
  }
  .entry:hover {
    background: var(--sel-bg);
  }
  .entry kbd {
    min-width: 1.75rem;
  }
</style>
