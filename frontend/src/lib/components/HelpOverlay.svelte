<script lang="ts">
  import { onMount } from "svelte";
  import { keymap, isReserved, formatKeys } from "../keys/keymap";
  import { ui } from "../stores/ui.svelte";
  import { trapFocus } from "../focus";

  let dialog: HTMLDivElement;
  let opener: Element | null = null;

  // Same table the key handler uses. Reserved keys are listed but dimmed.
  const groups = (() => {
    const m = new Map<string, typeof keymap>();
    for (const b of keymap) {
      if (b.action === "back" || b.context === "wizard") continue;
      m.set(b.group, [...(m.get(b.group) ?? []), b]);
    }
    return [...m.entries()];
  })();

  onMount(() => {
    opener = document.activeElement;
    dialog.focus();
    return () => (opener as HTMLElement | null)?.focus?.();
  });
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<div class="scrim" role="presentation" onclick={() => (ui.overlay = "none")}>
  <div
    class="card"
    role="dialog"
    aria-modal="true"
    aria-labelledby="help-title"
    tabindex="-1"
    bind:this={dialog}
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
  >
    <header>
      <h2 id="help-title">Keyboard shortcuts</h2>
      <button class="btn" type="button" onclick={() => (ui.overlay = "none")}
        >Close <kbd>Esc</kbd></button
      >
    </header>
    <p class="note">
      Everything also works with the mouse. Shortcuts are optional.
    </p>
    <div class="cols">
      {#each groups as [name, items] (name)}
        <section>
          <h3>{name === "Later" ? "Coming later" : name}</h3>
          <dl>
            {#each items as b (b.action + b.keys.join())}
              <div class:later={isReserved(b)}>
                <dt><kbd>{formatKeys(b.keys)}</kbd></dt>
                <dd>{b.desc}</dd>
              </div>
            {/each}
          </dl>
        </section>
      {/each}
    </div>
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
    width: min(52rem, 94vw);
    max-height: 86vh;
    overflow: auto;
    padding: 1.25rem 1.5rem;
    background: var(--bg-raised);
    border: 1px solid var(--line);
    border-radius: 10px;
    box-shadow: 0 12px 40px #0006;
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  h2 {
    font-size: 1.1rem;
  }
  .note {
    margin: 0.25rem 0 1rem;
    color: var(--fg-dim);
  }
  .cols {
    columns: 3 14rem;
    column-gap: 2rem;
  }
  section {
    break-inside: avoid;
    margin-bottom: 1rem;
  }
  h3 {
    margin-bottom: 0.25rem;
    font-size: var(--fs-small);
    color: var(--accent);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  dl {
    margin: 0;
  }
  dl > div {
    display: flex;
    gap: 0.6rem;
    padding: 0.15rem 0;
  }
  dt {
    min-width: 4.5rem;
  }
  dd {
    margin: 0;
  }
  .later {
    color: var(--fg-dim);
  }
</style>
