<script lang="ts">
  import { onMount } from "svelte";
  import { sync } from "../stores/sync.svelte";
  import SyncList from "./SyncList.svelte";

  onMount(() => {
    void sync.start();
    return () => sync.stop();
  });
</script>

<main class="main">
  <h1>Your mailbox is syncing</h1>
  <p class="dim">The folder and message views arrive in the next update.</p>
  <section class="card" aria-label="Sync progress">
    <p class="state">{sync.text}</p>
    {#if sync.fraction !== null}
      <progress value={sync.fraction} max="1" aria-label="Overall sync progress"
      ></progress>
    {/if}
    {#if sync.messages > 0}
      <p class="dim">{sync.messages.toLocaleString()} messages cached</p>
    {/if}
    {#if sync.error}
      <p class="err" role="alert">{sync.error}</p>
    {/if}
    <div class="list"><SyncList /></div>
  </section>
</main>

<style>
  .main {
    display: grid;
    align-content: center;
    justify-items: center;
    gap: 0.5rem;
    height: 100%;
    padding: 2rem;
    overflow: auto;
  }
  h1 {
    font-size: 1.3rem;
  }
  .dim {
    color: var(--fg-dim);
  }
  .card {
    width: min(30rem, 100%);
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
  .list {
    max-height: 18rem;
    overflow: auto;
  }
</style>
