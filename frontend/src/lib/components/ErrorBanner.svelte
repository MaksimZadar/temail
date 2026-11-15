<script lang="ts">
  import type { Friendly } from "../setup/errors";

  let {
    error,
    busy = false,
    onrecover,
  }: {
    error: Friendly | null;
    busy?: boolean;
    onrecover: () => void;
  } = $props();
</script>

<!-- The live region stays in the DOM so screen readers announce new errors. -->
<div class="slot" role="alert" aria-live="assertive">
  {#if error}
    <div class="banner">
      <strong>{error.title}</strong>
      <p>{error.body}</p>
      <button class="btn" type="button" disabled={busy} onclick={onrecover}
        >{error.action.label}</button
      >
    </div>
  {/if}
</div>

<style>
  .banner {
    margin-top: 1rem;
    padding: 0.75rem 1rem;
    border: 1px solid var(--danger);
    border-left-width: 4px;
    border-radius: 6px;
    background: var(--bg-raised);
    display: grid;
    gap: 0.35rem;
    justify-items: start;
  }
  strong {
    color: var(--danger);
  }
</style>
