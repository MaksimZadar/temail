<script lang="ts">
  import { continuations, formatKeys, isReserved } from "../keys/keymap";
  import { ui } from "../stores/ui.svelte";

  const items = $derived(
    continuations(ui.pending, [ui.context, "global"]).filter(
      (b) => !isReserved(b),
    ),
  );
</script>

{#if ui.whichKey && items.length > 0}
  <div class="wk" aria-hidden="true">
    {#each items as b (b.action + b.keys.join())}
      <div>
        <kbd>{formatKeys(b.keys.slice(ui.pending.length))}</kbd>
        {b.desc}
      </div>
    {/each}
  </div>
{/if}

<style>
  .wk {
    position: fixed;
    right: 0.75rem;
    bottom: 2.5rem;
    padding: 0.5rem 0.75rem;
    background: var(--bg-raised);
    border: 1px solid var(--line);
    border-radius: 8px;
    box-shadow: 0 6px 24px #0004;
    display: grid;
    gap: 0.15rem;
  }
</style>
