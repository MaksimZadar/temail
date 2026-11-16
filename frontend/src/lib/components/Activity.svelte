<script lang="ts">
  import { onMount } from "svelte";
  import { ui } from "../stores/ui.svelte";
  import { actions } from "../stores/actions.svelte";
  import { trapFocus } from "../focus";
  import { formatListDate } from "../mail/format";
  import { doneText } from "../mail/actions";
  import type { ActionType } from "../api";

  let dialog: HTMLDivElement;
  let opener: Element | null = null;

  onMount(() => {
    opener = document.activeElement;
    dialog.focus();
    void actions.loadActivity();
    return () => (opener as HTMLElement | null)?.focus?.();
  });

  const text = (e: (typeof actions.activity)[number]): string =>
    doneText(e.type as ActionType, e.count, e.params.folder ?? e.params.label);
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<div class="scrim" role="presentation" onclick={() => (ui.overlay = "none")}>
  <div
    class="card"
    role="dialog"
    aria-modal="true"
    aria-labelledby="act-title"
    tabindex="-1"
    bind:this={dialog}
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
  >
    <header>
      <h2 id="act-title">Recent activity</h2>
      <button class="btn" type="button" onclick={() => (ui.overlay = "none")}
        >Close <kbd>Esc</kbd></button
      >
    </header>
    {#if actions.activityError}
      <p class="err" role="alert">{actions.activityError}</p>
    {/if}
    <ul aria-label="Recent actions" aria-busy={actions.activityLoading}>
      {#each actions.activity as e (e.id)}
        <li class:undone={e.undone}>
          <div class="what">
            <span>{e.type === "undo" ? "Undo" : text(e)}</span>
            <span class="when">
              {formatListDate(e.ts)}{e.undone ? " · undone" : ""}{e.error
                ? ` · ${e.error}`
                : ""}
            </span>
          </div>
          <button
            class="btn"
            type="button"
            disabled={e.undone || !!e.error || e.type === "undo"}
            onclick={() => void actions.undoEntry(e.id)}>Undo</button
          >
        </li>
      {:else}
        {#if !actions.activityLoading}
          <li class="empty" role="status">Nothing has been done yet.</li>
        {/if}
      {/each}
    </ul>
  </div>
</div>

<style>
  .scrim {
    position: fixed;
    inset: 0;
    display: grid;
    place-items: center;
    background: var(--scrim);
    z-index: 10;
  }
  .card {
    width: min(32rem, 94vw);
    max-height: 80vh;
    overflow: auto;
    padding: 1rem 1.25rem;
    background: var(--bg-raised);
    border: 1px solid var(--line);
    border-radius: 10px;
    box-shadow: 0 12px 40px #0006;
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.5rem;
  }
  h2 {
    font-size: 1.1rem;
  }
  ul {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  li {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 1rem;
    padding: 0.4rem 0;
    border-bottom: 1px solid var(--line);
  }
  .what {
    display: grid;
  }
  .when,
  .empty {
    font-size: var(--fs-small);
    color: var(--fg-dim);
  }
  .undone .what span:first-child {
    text-decoration: line-through;
    color: var(--fg-dim);
  }
  .err {
    color: var(--danger);
  }
</style>
