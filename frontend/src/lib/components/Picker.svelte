<script lang="ts">
  import { onMount } from "svelte";
  import { ui } from "../stores/ui.svelte";
  import { mail } from "../stores/mail.svelte";
  import { actions } from "../stores/actions.svelte";
  import { trapFocus } from "../focus";
  import { pieces } from "../palette-rank";
  import {
    diffLabels,
    filterFolders,
    labelParam,
    labelStates,
    labelsOf,
    moveActionFor,
  } from "../mail/actions";
  import { rank } from "../palette-rank";

  let { mode }: { mode: "move" | "label" } = $props();
  const LIMIT = 5;

  const targets = actions.pending;
  let q = $state("");
  let sel = $state(0);
  let top = $state(0);
  let input: HTMLInputElement;
  let opener: Element | null = null;

  // ---- move ----
  const folders = $derived(filterFolders(mail.folders, q));

  // ---- labels ----
  const labels = labelsOf(mail.folders);
  const before = labelStates(labels, targets);
  let picked = $state<Record<string, boolean>>(
    Object.fromEntries(labels.map((l) => [l.name, before[l.name] === "all"])),
  );
  const touched = $state<Record<string, true>>({});
  const labelRows = $derived(
    rank(
      labels.map((l) => ({
        name: l.displayName,
        aliases: [l.name],
        run: () => {},
      })),
      q,
    ).map((r) => ({
      ...r,
      box: labels.find((l) => l.displayName === r.cmd.name)!,
    })),
  );

  const count = $derived(mode === "move" ? folders.length : labelRows.length);
  onMount(() => {
    opener = document.activeElement;
    input.focus();
    return () => (opener as HTMLElement | null)?.focus?.();
  });

  const close = (): void => {
    ui.overlay = "none";
  };

  function move(to: number): void {
    if (count === 0) return;
    sel = Math.min(count - 1, Math.max(0, to));
    if (sel < top) top = sel;
    else if (sel >= top + LIMIT) top = sel - LIMIT + 1;
  }

  function toggle(name: string): void {
    // "some" labels cycle: on -> off -> on; a partly-labelled row starts off
    // and the first click adds it everywhere.
    picked[name] = !picked[name];
    touched[name] = true;
  }

  function apply(): void {
    const { add, remove } = diffLabels(
      before,
      Object.fromEntries(Object.entries(picked).filter(([k]) => touched[k])),
    );
    const headers = targets;
    close();
    void (async () => {
      for (const n of add)
        await actions.run(
          "add_label",
          { label: labelParam(n) },
          { headers, label: labelParam(n) },
        );
      for (const n of remove)
        await actions.run(
          "remove_label",
          { label: labelParam(n) },
          { headers, label: labelParam(n) },
        );
    })();
  }

  function chooseFolder(i: number): void {
    const r = folders[i];
    if (!r) return;
    const f = mail.folders.find((x) => x.displayName === r.cmd.name);
    if (!f) return;
    const a = moveActionFor(f);
    const headers = targets;
    close();
    void actions.run(a.type, a.params, { headers, label: f.displayName });
  }

  function onkey(e: KeyboardEvent): void {
    if (e.key === "Escape") close();
    else if (e.key === "ArrowDown" || (e.ctrlKey && e.key === "n"))
      move(sel + 1);
    else if (e.key === "ArrowUp" || (e.ctrlKey && e.key === "p")) move(sel - 1);
    else if (e.key === "Enter") {
      if (mode === "move") chooseFolder(sel);
      else apply();
    } else if (e.key === " " && mode === "label" && e.ctrlKey) {
      const r = labelRows[sel];
      if (r) toggle(r.box.name);
    } else return;
    e.preventDefault();
    e.stopPropagation();
  }
  const title = $derived(
    mode === "move"
      ? `Move ${targets.length === 1 ? "1 message" : `${targets.length} messages`} to…`
      : "Labels",
  );
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<div class="scrim" role="presentation" onclick={close}>
  <div
    class="card"
    role="dialog"
    aria-modal="true"
    aria-label={title}
    tabindex="-1"
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
  >
    <p class="head">{title}</p>
    <input
      bind:this={input}
      bind:value={q}
      oninput={() => {
        sel = 0;
        top = 0;
      }}
      onkeydown={onkey}
      placeholder={mode === "move" ? "Type a folder" : "Type a label"}
      aria-label={mode === "move" ? "Folder" : "Label"}
      role="combobox"
      aria-expanded="true"
      aria-controls="pick-list"
      aria-autocomplete="list"
      autocomplete="off"
      spellcheck="false"
    />
    <ul id="pick-list" role="listbox" aria-label={title}>
      {#if mode === "move"}
        {#each folders.slice(top, top + LIMIT) as r, k (r.cmd.name)}
          {@const i = top + k}
          <li role="option" aria-selected={i === sel}>
            <button
              type="button"
              class:active={i === sel}
              tabindex="-1"
              onclick={() => chooseFolder(i)}
              onmousemove={() => (sel = i)}
            >
              {#each pieces(r.cmd.name, r.hits) as p, pi (pi)}
                {#if p.hit}<mark>{p.text}</mark>{:else}{p.text}{/if}
              {/each}
            </button>
          </li>
        {:else}
          <li class="empty" role="status">No matching folders</li>
        {/each}
      {:else}
        {#each labelRows.slice(top, top + LIMIT) as r, k (r.box.name)}
          {@const i = top + k}
          {@const was = before[r.box.name]}
          <li role="option" aria-selected={i === sel}>
            <button
              type="button"
              role="checkbox"
              aria-checked={!touched[r.box.name] && was === "some"
                ? "mixed"
                : picked[r.box.name]}
              class:active={i === sel}
              tabindex="-1"
              onclick={() => toggle(r.box.name)}
              onmousemove={() => (sel = i)}
            >
              <span class="box" aria-hidden="true">
                {#if !touched[r.box.name] && was === "some"}–{:else if picked[r.box.name]}✓{/if}
              </span>
              {r.cmd.name}
            </button>
          </li>
        {:else}
          <li class="empty" role="status">No labels yet</li>
        {/each}
      {/if}
    </ul>
    {#if count > LIMIT}
      <p class="count" aria-hidden="true">
        {top + 1}–{Math.min(count, top + LIMIT)} of {count} · ↑↓ for more
      </p>
    {/if}
    {#if mode === "label"}
      <footer>
        <span class="hint"
          >Click or <kbd>Ctrl</kbd>+<kbd>Space</kbd> to tick</span
        >
        <button class="btn" type="button" onclick={apply}
          >Apply <kbd>Enter</kbd></button
        >
      </footer>
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
    width: min(26rem, 92vw);
    align-self: flex-start;
    background: var(--bg-raised);
    border: 1px solid var(--line);
    border-radius: 10px;
    box-shadow: 0 12px 40px #0006;
    overflow: hidden;
  }
  .head {
    margin: 0;
    padding: 0.6rem 0.9rem 0;
    font-size: var(--fs-small);
    color: var(--fg-dim);
  }
  input {
    width: 100%;
    padding: 0.5rem 0.9rem 0.7rem;
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
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    height: 2rem;
    text-align: left;
    padding: 0 0.6rem;
    background: none;
    border: 0;
    border-radius: 5px;
    overflow: hidden;
    white-space: nowrap;
  }
  li button.active {
    background: var(--sel-bg);
  }
  .box {
    display: inline-grid;
    place-items: center;
    flex: none;
    width: 1rem;
    height: 1rem;
    border: 1px solid var(--fg-dim);
    border-radius: 3px;
    font-size: 0.75rem;
    line-height: 1;
    color: var(--accent);
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
    font-size: var(--fs-small);
  }
  footer {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 0.5rem 0.75rem;
    border-top: 1px solid var(--line);
  }
  .hint {
    font-size: var(--fs-small);
    color: var(--fg-dim);
  }
</style>
