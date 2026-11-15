<script lang="ts">
  import { onMount } from "svelte";
  import { ui } from "../stores/ui.svelte";

  interface Cmd {
    name: string;
    run: () => void;
  }
  const commands: Cmd[] = [
    { name: "Show keyboard shortcuts", run: () => (ui.overlay = "help") },
    { name: "Theme: follow system", run: () => ui.setTheme("auto") },
    { name: "Theme: light", run: () => ui.setTheme("light") },
    { name: "Theme: dark", run: () => ui.setTheme("dark") },
  ];

  let q = $state("");
  let sel = $state(0);
  let input: HTMLInputElement;
  let opener: Element | null = null;
  const shown = $derived(
    commands.filter((c) =>
      c.name.toLowerCase().includes(q.trim().toLowerCase()),
    ),
  );

  onMount(() => {
    opener = document.activeElement;
    input.focus();
    return () => (opener as HTMLElement | null)?.focus?.();
  });

  function choose(c: Cmd | undefined): void {
    if (!c) return;
    ui.overlay = "none";
    c.run();
  }
  function onkey(e: KeyboardEvent): void {
    e.stopPropagation();
    if (e.key === "Escape") ui.overlay = "none";
    else if (e.key === "ArrowDown") {
      sel = Math.min(sel + 1, shown.length - 1);
      e.preventDefault();
    } else if (e.key === "ArrowUp") {
      sel = Math.max(sel - 1, 0);
      e.preventDefault();
    } else if (e.key === "Enter") choose(shown[sel]);
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<div class="scrim" role="presentation" onclick={() => (ui.overlay = "none")}>
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <div
    class="card"
    role="dialog"
    aria-modal="true"
    aria-label="Commands"
    tabindex="-1"
    onclick={(e) => e.stopPropagation()}
  >
    <input
      bind:this={input}
      bind:value={q}
      oninput={() => (sel = 0)}
      onkeydown={onkey}
      placeholder="Type a command"
      aria-label="Command"
      role="combobox"
      aria-expanded="true"
      aria-controls="cmd-list"
      aria-activedescendant={shown[sel] ? `cmd-${sel}` : undefined}
    />
    <ul id="cmd-list" role="listbox">
      {#each shown as c, i (c.name)}
        <li id="cmd-{i}" role="option" aria-selected={i === sel}>
          <button
            type="button"
            class:active={i === sel}
            tabindex="-1"
            onclick={() => choose(c)}>{c.name}</button
          >
        </li>
      {:else}
        <li class="empty">No matching command</li>
      {/each}
    </ul>
  </div>
</div>

<style>
  .scrim {
    position: fixed;
    inset: 0;
    display: flex;
    justify-content: center;
    padding-top: 15vh;
    background: var(--scrim);
    z-index: 10;
  }
  .card {
    width: min(30rem, 92vw);
    align-self: flex-start;
    background: var(--bg-raised);
    border: 1px solid var(--line);
    border-radius: 10px;
    box-shadow: 0 12px 40px #0006;
    overflow: hidden;
  }
  input {
    width: 100%;
    padding: 0.7rem 0.9rem;
    background: transparent;
    border: 0;
    border-bottom: 1px solid var(--line);
    font-size: var(--fs-body);
  }
  input:focus-visible {
    outline-offset: -2px;
  }
  ul {
    margin: 0;
    padding: 0.25rem;
    list-style: none;
  }
  li button {
    width: 100%;
    text-align: left;
    padding: 0.4rem 0.6rem;
    background: none;
    border: 0;
    border-radius: 5px;
  }
  li button:hover,
  li button.active {
    background: var(--sel-bg);
  }
  .empty {
    padding: 0.5rem 0.6rem;
    color: var(--fg-dim);
  }
</style>
