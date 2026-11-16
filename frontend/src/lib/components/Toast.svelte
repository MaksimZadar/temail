<script lang="ts">
  import { actions } from "../stores/actions.svelte";
  const t = $derived(actions.toast);
</script>

{#if t}
  <div
    class="toast"
    class:error={t.kind === "error"}
    role={t.kind === "error" ? "alert" : "status"}
    onmouseenter={() => actions.holdToast()}
    onmouseleave={() => actions.releaseToast()}
    onfocusin={() => actions.holdToast()}
    onfocusout={() => actions.releaseToast()}
  >
    <span class="text">{t.text}</span>
    {#if t.undo}
      <button
        class="undo"
        type="button"
        title="Undo (z)"
        onclick={() => {
          actions.dismissToast();
          void actions.undoLast();
        }}>Undo <kbd>z</kbd></button
      >
    {/if}
    <button
      class="x"
      type="button"
      aria-label="Dismiss"
      title="Dismiss"
      onclick={() => actions.dismissToast()}>×</button
    >
  </div>
{/if}

<style>
  .toast {
    position: fixed;
    left: 50%;
    bottom: 3rem;
    transform: translateX(-50%);
    z-index: 20;
    display: flex;
    align-items: center;
    gap: 0.75rem;
    max-width: min(34rem, 92vw);
    padding: 0.5rem 0.6rem 0.5rem 0.9rem;
    background: var(--bg-raised);
    border: 1px solid var(--line);
    border-left: 3px solid var(--accent);
    border-radius: 8px;
    box-shadow: 0 8px 28px #0005;
  }
  .toast.error {
    border-left-color: var(--danger);
  }
  .text {
    flex: 1;
  }
  .undo {
    color: var(--accent);
    font-weight: 600;
    background: none;
    border: 0;
    border-radius: 5px;
    padding: 0.25rem 0.5rem;
  }
  .undo:hover {
    background: var(--sel-bg);
  }
  .x {
    background: none;
    border: 0;
    color: var(--fg-dim);
    font-size: 1.1rem;
    line-height: 1;
    padding: 0.2rem 0.4rem;
    border-radius: 5px;
  }
  @media (prefers-reduced-motion: no-preference) {
    .toast {
      animation: rise 0.16s ease-out;
    }
    @keyframes rise {
      from {
        opacity: 0;
        transform: translate(-50%, 8px);
      }
    }
  }
</style>
