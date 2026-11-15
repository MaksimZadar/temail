<script lang="ts">
  import { sync } from "../stores/sync.svelte";

  const label = {
    syncing: "Syncing",
    waiting: "Waiting",
    done: "Up to date",
    failed: "Couldn't sync",
  } as const;
</script>

{#if sync.sorted.length === 0}
  <p class="empty">No folders yet.</p>
{:else}
  <ul class="list" aria-label="Folders">
    {#each sync.sorted as r (r.name)}
      <li class="row {r.state}">
        <span class="name">{r.display}</span>
        <span class="state">{label[r.state]}</span>
        <span class="count">
          {#if r.state === "syncing" && r.headersTotal > 0 && r.bodiesTotal === 0}
            {r.headersDone} of {r.headersTotal} new
          {:else if r.state === "syncing" && r.bodiesTotal > 0}
            {r.bodiesDone} of {r.bodiesTotal} messages
          {:else if r.messages > 0}
            {r.messages.toLocaleString()} messages
          {/if}
        </span>
        <span
          class="bar"
          role="progressbar"
          aria-label="{r.display} progress"
          aria-valuemin="0"
          aria-valuemax="100"
          aria-valuenow={Math.round(r.fraction * 100)}
        >
          <span style:width="{r.fraction * 100}%"></span>
        </span>
        {#if r.state === "failed" && r.error}
          <span class="reason">{r.error}</span>
        {/if}
      </li>
    {/each}
  </ul>
{/if}

<style>
  .list {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .row {
    display: grid;
    grid-template-columns: 1fr auto auto;
    column-gap: 0.75rem;
    row-gap: 0.2rem;
    align-items: baseline;
    padding: 0.4rem 0.25rem;
    border-bottom: 1px solid var(--line);
  }
  .row:last-child {
    border-bottom: 0;
  }
  .name {
    font-weight: 600;
  }
  .state,
  .count {
    color: var(--fg-dim);
    font-size: var(--fs-small);
  }
  .syncing .state {
    color: var(--accent);
  }
  .failed .state,
  .reason {
    color: var(--danger);
  }
  .bar {
    grid-column: 1 / -1;
    height: 3px;
    background: var(--line);
    border-radius: 2px;
    overflow: hidden;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--accent);
    transition: width 0.25s ease-out;
  }
  .failed .bar span {
    background: var(--danger);
  }
  .reason {
    grid-column: 1 / -1;
    font-size: var(--fs-small);
  }
  .empty {
    color: var(--fg-dim);
  }
</style>
