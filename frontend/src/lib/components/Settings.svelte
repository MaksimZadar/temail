<script lang="ts">
  import { onMount, tick } from "svelte";
  import { ui } from "../stores/ui.svelte";
  import { llm } from "../llm/store.svelte";
  import { ProviderForm } from "../llm/form.svelte";
  import { PRESETS, type PresetId } from "../llm/presets";
  import ProviderCard from "./ProviderCard.svelte";
  import ProviderFormView from "./ProviderForm.svelte";
  import ConfirmDialog from "./ConfirmDialog.svelte";
  import type { LLMProvider } from "../api";

  type Confirm =
    | { kind: "delete"; p: LLMProvider }
    | { kind: "enable"; p: LLMProvider }
    | { kind: "form-enable"; test: boolean };

  let title = $state<HTMLHeadingElement>();
  let mode = $state<"list" | "choose" | "form">("list");
  let confirm = $state<Confirm | null>(null);
  let opener: HTMLElement | null = null;

  const form = new ProviderForm(
    () => llm.providers.map((p) => p.name),
    (name, test) => {
      void llm.load().then(() => {
        if (test) void llm.test(name);
      });
    },
  );

  const noProviders = $derived(llm.loaded && llm.providers.length === 0);

  onMount(() => {
    void llm.load();
    void tick().then(() => title?.focus());
    const off = ui.register("back", () => {
      if (confirm) confirm = null;
      else if (mode !== "list") close();
      else ui.screen = "mail";
    });
    return off;
  });

  function remember(): void {
    opener = document.activeElement as HTMLElement | null;
  }
  async function close(): Promise<void> {
    mode = "list";
    form.apiKey = "";
    await tick();
    (opener?.isConnected ? opener : title)?.focus();
  }

  function add(id?: PresetId): void {
    if (id) {
      form.startCreate(id);
      mode = "form";
    } else {
      remember();
      mode = "choose";
    }
  }
  function edit(p: LLMProvider): void {
    remember();
    form.startEdit(p);
    mode = "form";
  }

  async function submit(test: boolean): Promise<void> {
    const r = await form.submit({ test });
    if (r === "saved") await close();
    else if (r === "confirm") confirm = { kind: "form-enable", test };
  }

  async function toggle(p: LLMProvider, want: boolean): Promise<void> {
    if (want && p.kind === "cloud") confirm = { kind: "enable", p };
    else await llm.setEnabled(p, want);
  }

  async function onConfirm(): Promise<void> {
    const c = confirm;
    confirm = null;
    if (!c) return;
    if (c.kind === "delete") await llm.remove(c.p.name);
    else if (c.kind === "enable") await llm.setEnabled(c.p, true);
    else {
      const r = await form.submit({ test: c.test, confirmed: true });
      if (r === "saved") await close();
    }
  }

  function recover(p: LLMProvider, a: "edit" | "enable" | "retry"): void {
    if (a === "edit") edit(p);
    else if (a === "enable") confirm = { kind: "enable", p };
    else void llm.test(p.name);
  }
</script>

<div class="page">
  <div class="col">
    <header>
      <button
        class="btn"
        type="button"
        title="Back to mail (Esc)"
        onclick={() => (ui.screen = "mail")}>← Mail</button
      >
      <h1 tabindex="-1" bind:this={title}>Settings</h1>
    </header>

    <section aria-labelledby="ai-h">
      <h2 id="ai-h">AI providers</h2>
      <p class="lead">
        temail can use an AI model to suggest labels and drafts. Local providers
        like Ollama stay on this computer. Cloud providers receive email content
        when you use AI features, and nothing is sent until you turn one on.
      </p>

      {#if llm.loadError}
        <p class="err" role="alert">
          {llm.loadError}
          <button class="btn small" type="button" onclick={() => llm.load()}
            >Try again</button
          >
        </p>
      {/if}
      {#if llm.actionError}
        <p class="err" role="alert">{llm.actionError}</p>
      {/if}

      {#if mode === "form"}
        <ProviderFormView {form} oncancel={close} onsubmit={submit} />
      {:else}
        {#if mode === "choose" || noProviders}
          <div class="choose" role="group" aria-label="Add a provider">
            {#if noProviders}
              <p class="empty">
                No AI provider yet. Add Ollama to keep everything on this
                computer, or OpenRouter to use a cloud model.
              </p>
            {/if}
            <div class="presets">
              {#each PRESETS as p (p.id)}
                <button class="preset" type="button" onclick={() => add(p.id)}>
                  <strong>{p.label}</strong>
                  <span>{p.blurb}</span>
                </button>
              {/each}
            </div>
            {#if mode === "choose"}
              <button class="btn" type="button" onclick={close}>Cancel</button>
            {/if}
          </div>
        {:else if llm.loaded}
          <button class="btn primary add" type="button" onclick={() => add()}
            >Add provider</button
          >
        {/if}

        {#if llm.providers.length > 0}
          <ul class="list" aria-label="Providers">
            {#each llm.providers as p (p.name)}
              <ProviderCard
                provider={p}
                test={llm.tests[p.name]}
                ontest={() => llm.test(p.name)}
                onedit={() => edit(p)}
                ondefault={() => llm.setDefault(p.name)}
                ondelete={() => {
                  remember();
                  confirm = { kind: "delete", p };
                }}
                ontoggle={(w) => toggle(p, w)}
                onrecover={(a) => recover(p, a)}
              />
            {/each}
          </ul>
        {/if}
      {/if}
    </section>
  </div>
</div>

{#if confirm?.kind === "delete"}
  <ConfirmDialog
    title="Delete {confirm.p.name}?"
    body="This removes the provider and its saved API key. It can't be undone."
    confirmLabel="Delete"
    danger
    onconfirm={onConfirm}
    oncancel={() => (confirm = null)}
  />
{:else if confirm?.kind === "enable"}
  <ConfirmDialog
    title="Turn on {confirm.p.name}?"
    body="Cloud providers receive email content when you use AI features. Nothing is sent until you do, and you can turn it off again at any time."
    confirmLabel="Turn on"
    onconfirm={onConfirm}
    oncancel={() => (confirm = null)}
  />
{:else if confirm?.kind === "form-enable"}
  <ConfirmDialog
    title="Turn on {form.name.trim()}?"
    body="Cloud providers receive email content when you use AI features. Nothing is sent until you do, and you can turn it off again at any time."
    confirmLabel="Save and turn on"
    onconfirm={onConfirm}
    oncancel={() => (confirm = null)}
  />
{/if}

<style>
  .page {
    height: 100%;
    overflow-y: auto;
    background: var(--bg-pane);
    padding: 1.25rem 1rem 2rem;
  }
  .col {
    width: min(46rem, 100%);
    margin: 0 auto;
    display: grid;
    gap: 1rem;
  }
  header {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }
  h1 {
    font-size: 1.3rem;
  }
  h1:focus {
    outline: none;
  }
  h2 {
    font-size: 1.05rem;
    margin-bottom: 0.25rem;
  }
  section {
    display: grid;
    gap: 0.8rem;
  }
  .lead,
  .empty {
    color: var(--fg-dim);
  }
  .err {
    color: var(--danger);
  }
  .add {
    justify-self: start;
  }
  .small {
    padding: 0.1rem 0.6rem;
  }
  .list {
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.75rem;
  }
  .choose {
    display: grid;
    gap: 0.75rem;
    justify-items: start;
  }
  .presets {
    display: grid;
    gap: 0.6rem;
    grid-template-columns: repeat(auto-fit, minmax(13rem, 1fr));
    width: 100%;
  }
  .preset {
    display: grid;
    gap: 0.25rem;
    text-align: left;
    padding: 0.8rem 1rem;
    background: var(--bg);
    border: 1px solid var(--line);
    border-radius: 8px;
  }
  .preset:hover {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .preset span {
    color: var(--fg-dim);
    font-size: var(--fs-small);
  }
</style>
