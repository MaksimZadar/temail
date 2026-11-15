<script lang="ts">
  import { onMount } from "svelte";
  import { sync } from "../stores/sync.svelte";

  onMount(() => {
    void sync.start();
    return () => sync.stop();
  });
</script>

<main class="main">
  <h1>Your mailbox is syncing</h1>
  <p class="dim">The folder and message views arrive in the next update.</p>
  <div class="card" role="status" aria-live="polite">
    <p class="state">{sync.text}</p>
    {#if sync.fraction !== null}
      <progress value={sync.fraction} max="1" aria-label="Sync progress"
      ></progress>
    {/if}
    {#if sync.progress && sync.progress.bodiesTotal > 0}
      <p class="dim">
        Messages: {sync.progress.bodiesDone} of {sync.progress.bodiesTotal}
      </p>
    {/if}
    {#if sync.error}
      <p class="err" role="alert">{sync.error}</p>
    {/if}
    {#if sync.result}
      <p class="dim">
        {sync.result.headersFetched} new headers across {sync.result.mailboxes} folders.
      </p>
      {#if sync.result.mailboxErrors?.length}
        <p class="err">
          Couldn't sync: {sync.result.mailboxErrors
            .map((m) => m.mailbox)
            .join(", ")}
        </p>
      {/if}
    {/if}
  </div>
</main>

<style>
  .main {
    display: grid;
    align-content: center;
    justify-items: center;
    gap: 0.5rem;
    height: 100%;
    padding: 2rem;
  }
  h1 {
    font-size: 1.3rem;
  }
  .dim {
    color: var(--fg-dim);
  }
  .card {
    min-width: 22rem;
    margin-top: 1rem;
    padding: 1rem 1.25rem;
    background: var(--bg-raised);
    border: 1px solid var(--line);
    border-radius: 8px;
    display: grid;
    gap: 0.5rem;
  }
  .state {
    font-weight: 600;
  }
  progress {
    width: 100%;
    accent-color: var(--accent);
  }
  .err {
    color: var(--danger);
  }
</style>
