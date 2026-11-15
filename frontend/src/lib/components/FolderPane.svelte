<script lang="ts">
  import { mail } from "../stores/mail.svelte";
  import { layout } from "../stores/layout.svelte";
  import { GROUPS } from "../mail/folders";
  import type { MailboxDTO } from "../api";

  const focused = $derived(layout.focus === "folders");

  const groups = $derived(
    GROUPS.map((g) => ({
      title: g.title,
      items: mail.folders.filter((f) => g.kinds.includes(f.kind)),
    })).filter((g) => g.items.length > 0),
  );

  function pick(f: MailboxDTO): void {
    mail.folderCursor = f.name;
    layout.setFocus("list");
    void mail.selectFolder(f.name);
  }
</script>

<nav class="pane" class:focused aria-label="Folders" data-pane="folders">
  {#if !mail.foldersLoaded}
    <p class="note">Loading folders…</p>
  {:else if mail.foldersError}
    <p class="note err" role="alert">{mail.foldersError}</p>
    <button class="btn" type="button" onclick={() => mail.loadFolders()}
      >Try again</button
    >
  {:else if mail.folders.length === 0}
    <p class="note">No folders yet. They appear after the first sync.</p>
  {:else}
    {#each groups as g (g.title)}
      {#if g.title}<h2>{g.title}</h2>{/if}
      <ul>
        {#each g.items as f (f.name)}
          <li>
            <button
              type="button"
              class="folder"
              class:current={f.name === mail.current}
              class:cursor={focused && f.name === mail.folderCursor}
              aria-current={f.name === mail.current ? "true" : undefined}
              aria-label="{f.displayName}, {f.messages} messages"
              onclick={() => pick(f)}
            >
              <span class="name">{f.displayName}</span>
              {#if f.messages > 0}<span class="count"
                  >{f.messages.toLocaleString()}</span
                >{/if}
            </button>
          </li>
        {/each}
      </ul>
    {/each}
  {/if}
</nav>

<style>
  .pane {
    height: 100%;
    overflow-y: auto;
    padding: 0.5rem 0.4rem;
    background: var(--bg-pane);
    border-top: 2px solid transparent;
  }
  .pane.focused {
    border-top-color: var(--accent);
  }
  ul {
    margin: 0 0 0.5rem;
    padding: 0;
    list-style: none;
  }
  h2 {
    margin: 0.75rem 0.5rem 0.25rem;
    font-size: var(--fs-small);
    font-weight: 600;
    color: var(--fg-dim);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .folder {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    height: var(--row-h);
    padding: 0 0.6rem;
    text-align: left;
    background: none;
    border: 0;
    border-radius: 6px;
    position: relative;
  }
  .folder:hover {
    background: var(--accent-soft);
  }
  .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .count {
    color: var(--fg-dim);
    font-size: var(--fs-small);
  }
  .current {
    background: var(--sel-bg);
    font-weight: 600;
  }
  .current::before {
    content: "";
    position: absolute;
    left: 0;
    top: 4px;
    bottom: 4px;
    width: 2px;
    border-radius: 2px;
    background: var(--accent);
  }
  .cursor {
    outline: 2px solid var(--accent);
    outline-offset: -2px;
  }
  .note {
    padding: 0.5rem;
    color: var(--fg-dim);
  }
  .err {
    color: var(--danger);
  }
</style>
