<script lang="ts">
  import { onMount } from "svelte";
  import * as api from "../api";
  import { ui } from "../stores/ui.svelte";
  import { mail } from "../stores/mail.svelte";
  import { layout } from "../stores/layout.svelte";
  import { trapFocus } from "../focus";
  import { pieces, rank, type Cmd } from "../palette-rank";

  /** At most this many results are on screen at once. */
  const LIMIT = 5;

  const base: Cmd[] = [
    {
      name: "Refresh mail",
      aliases: ["sync", "fetch", "reload"],
      run: () =>
        void api.syncStart().catch(() => ui.flash("Couldn't start a sync")),
    },
    {
      name: "Show keyboard shortcuts",
      aliases: ["help", "keys", "?"],
      run: () => (ui.overlay = "help"),
    },
    {
      name: "Show or hide folders",
      aliases: ["sidebar", "panes"],
      run: () => layout.toggleFolders(),
    },
    {
      name: "Plain text or formatted",
      aliases: ["html", "toggle"],
      run: () => (mail.plain = !mail.plain),
    },
    {
      name: "Theme: follow system",
      aliases: ["auto"],
      run: () => ui.setTheme("auto"),
    },
    {
      name: "Theme: light",
      aliases: ["day", "bright"],
      run: () => ui.setTheme("light"),
    },
    { name: "Theme: dark", aliases: ["night"], run: () => ui.setTheme("dark") },
  ];
  const commands = $derived<Cmd[]>([
    ...mail.folders.map((f) => ({
      name: `Go to ${f.displayName}`,
      aliases: [f.name, "folder", "open"],
      run: () => void mail.selectFolder(f.name),
    })),
    ...base,
  ]);

  let q = $state(ui.paletteQuery);
  let sel = $state(0);
  let top = $state(0);
  let input: HTMLInputElement;
  let dialog: HTMLDivElement;
  let opener: Element | null = null;

  const results = $derived(rank(commands, q));
  const visible = $derived(results.slice(top, top + LIMIT));

  onMount(() => {
    opener = document.activeElement;
    ui.paletteQuery = "";
    input.focus();
    return () => (opener as HTMLElement | null)?.focus?.();
  });

  function move(to: number): void {
    if (results.length === 0) return;
    sel = Math.min(results.length - 1, Math.max(0, to));
    if (sel < top) top = sel;
    else if (sel >= top + LIMIT) top = sel - LIMIT + 1;
  }
  function choose(i: number): void {
    const r = results[i];
    if (!r) return;
    ui.overlay = "none";
    r.cmd.run();
  }
  function onkey(e: KeyboardEvent): void {
    if (e.key === "Escape") {
      ui.overlay = "none";
    } else if (e.key === "ArrowDown" || (e.ctrlKey && e.key === "n")) {
      move(sel + 1);
    } else if (e.key === "ArrowUp" || (e.ctrlKey && e.key === "p")) {
      move(sel - 1);
    } else if (e.key === "PageDown") {
      move(sel + LIMIT);
    } else if (e.key === "PageUp") {
      move(sel - LIMIT);
    } else if (e.key === "Enter") {
      choose(sel);
    } else return;
    e.preventDefault();
    e.stopPropagation();
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<div class="scrim" role="presentation" onclick={() => (ui.overlay = "none")}>
  <div
    class="card"
    role="dialog"
    aria-modal="true"
    aria-label="Commands"
    tabindex="-1"
    bind:this={dialog}
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
  >
    <input
      bind:this={input}
      bind:value={q}
      oninput={() => {
        sel = 0;
        top = 0;
      }}
      onkeydown={onkey}
      placeholder="Type a command"
      aria-label="Command"
      role="combobox"
      aria-expanded="true"
      aria-controls="cmd-list"
      aria-autocomplete="list"
      aria-activedescendant={results[sel] ? `cmd-${sel}` : undefined}
      autocomplete="off"
      spellcheck="false"
    />
    <ul id="cmd-list" role="listbox" aria-label="Matching commands">
      {#each visible as r, k (r.cmd.name)}
        {@const i = top + k}
        <li id="cmd-{i}" role="option" aria-selected={i === sel}>
          <button
            type="button"
            class:active={i === sel}
            tabindex="-1"
            onclick={() => choose(i)}
            onmousemove={() => (sel = i)}
          >
            {#each pieces(r.cmd.name, r.hits) as p, pi (pi)}
              {#if p.hit}<mark>{p.text}</mark>{:else}{p.text}{/if}
            {/each}
          </button>
        </li>
      {:else}
        <li class="empty" role="status">No matching commands</li>
      {/each}
    </ul>
    {#if results.length > LIMIT}
      <p class="count" aria-hidden="true">
        {top + 1}–{Math.min(results.length, top + LIMIT)} of {results.length}
        · ↑↓ for more
      </p>
    {/if}
  </div>
</div>

<style>
  .scrim {
    position: fixed;
    inset: 0;
    display: flex;
    justify-content: center;
    padding-top: 12vh;
    background: var(--scrim);
    z-index: 10;
  }
  .card {
    width: min(30rem, 92vw);
    align-self: flex-start;
    max-height: 70vh;
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
    height: 2rem;
    text-align: left;
    padding: 0 0.6rem;
    background: none;
    border: 0;
    border-radius: 5px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  li button.active {
    background: var(--sel-bg);
  }
  mark {
    background: none;
    color: var(--accent);
    font-weight: 700;
  }
  .empty,
  .count {
    padding: 0.5rem 0.6rem;
    color: var(--fg-dim);
  }
  .count {
    margin: 0;
    padding-top: 0.2rem;
    border-top: 1px solid var(--line);
    font-size: var(--fs-small);
  }
</style>
