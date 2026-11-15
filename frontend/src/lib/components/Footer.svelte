<script lang="ts">
  import { ui } from "../stores/ui.svelte";
  import { sync } from "../stores/sync.svelte";
  import { app } from "../stores/app.svelte";
  import { formatKeys } from "../keys/keymap";
</script>

<footer class="footer">
  <div class="left" role="status" aria-live="polite">
    {#if app.route === "main"}
      <span
        class="dot"
        class:busy={sync.running}
        class:bad={!!sync.error}
        aria-hidden="true"
      ></span>
      <span>{sync.text}</span>
      {#if sync.fraction !== null}
        <span class="bar" aria-hidden="true"
          ><span style:width="{sync.fraction * 100}%"></span></span
        >
      {/if}
    {/if}
    {#if ui.hint}<span class="hint">{ui.hint}</span>{/if}
  </div>
  <div class="right">
    {#if ui.pending.length > 0 || ui.count}
      <span class="chip" aria-hidden="true"
        >{ui.count ?? ""}{formatKeys(ui.pending)}…</span
      >
    {/if}
    <button
      class="link"
      type="button"
      onclick={() => (ui.overlay = "help")}
      title="Keyboard shortcuts (?)"
    >
      Shortcuts <kbd>?</kbd>
    </button>
  </div>
</footer>

<style>
  .footer {
    display: flex;
    justify-content: space-between;
    align-items: center;
    height: 1.75rem;
    padding: 0 0.75rem;
    background: var(--bg-pane);
    border-top: 1px solid var(--line);
    color: var(--fg-dim);
    font-size: var(--fs-small);
  }
  .left,
  .right {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .dot {
    width: 0.5rem;
    height: 0.5rem;
    border-radius: 50%;
    background: var(--ok);
  }
  .dot.busy {
    background: var(--accent);
    animation: pulse 1.4s ease-in-out infinite;
  }
  .dot.bad {
    background: var(--danger);
  }
  @keyframes pulse {
    50% {
      opacity: 0.35;
    }
  }
  .bar {
    width: 6rem;
    height: 3px;
    background: var(--line);
    border-radius: 2px;
    overflow: hidden;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  .hint {
    color: var(--fg);
  }
  .chip {
    padding: 0 0.4rem;
    border-radius: 3px;
    background: var(--accent-soft);
    color: var(--fg);
  }
  .link {
    background: none;
    border: 0;
    padding: 0.1rem 0.3rem;
    border-radius: 3px;
    color: inherit;
  }
  .link:hover {
    color: var(--fg);
    background: var(--accent-soft);
  }
</style>
