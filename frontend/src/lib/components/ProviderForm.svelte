<script lang="ts">
  import { onMount, tick } from "svelte";
  import { PRESETS, type PresetId } from "../llm/presets";
  import type { ProviderForm } from "../llm/form.svelte";
  import ErrorBanner from "./ErrorBanner.svelte";

  let {
    form,
    oncancel,
    onsubmit,
  }: {
    form: ProviderForm;
    oncancel: () => void;
    onsubmit: (test: boolean) => void;
  } = $props();

  let heading = $state<HTMLHeadingElement>();
  let keyInput = $state<HTMLInputElement>();
  const errs = $derived(form.showErrors ? form.errors : {});
  const cloud = $derived(form.kind === "cloud");

  onMount(
    () =>
      void tick().then(() =>
        form.keyNeeded ? keyInput?.focus() : heading?.focus(),
      ),
  );

  const pick = (id: PresetId): void => form.applyPreset(id);

  async function recover(): Promise<void> {
    const wasKey = form.keyNeeded;
    form.error = null;
    if (wasKey) {
      await tick();
      keyInput?.focus();
    }
  }
  const banner = $derived(
    form.error
      ? {
          title: form.error.title,
          body: form.error.body,
          action: {
            label: form.keyNeeded ? "Enter the key" : "Dismiss",
            to: "retry" as const,
          },
        }
      : null,
  );
</script>

<form
  class="form"
  aria-labelledby="pf-title"
  onsubmit={(e) => {
    e.preventDefault();
    onsubmit(false);
  }}
>
  <h2 id="pf-title" tabindex="-1" bind:this={heading}>
    {form.mode === "create" ? "Add a provider" : `Edit ${form.name}`}
  </h2>

  {#if form.mode === "create"}
    <fieldset class="presets">
      <legend>Start from</legend>
      <div class="seg">
        {#each PRESETS as p (p.id)}
          <button
            type="button"
            aria-pressed={form.preset === p.id}
            onclick={() => pick(p.id)}>{p.label}</button
          >
        {/each}
      </div>
      <p class="help">
        {PRESETS.find((p) => p.id === form.preset)?.blurb}
      </p>
    </fieldset>
  {/if}

  <div class="field">
    <label for="pf-name">Name</label>
    <input
      id="pf-name"
      bind:value={form.name}
      disabled={form.mode === "edit"}
      autocomplete="off"
      spellcheck="false"
      aria-invalid={!!errs.name}
      aria-describedby="pf-name-h"
    />
    <small id="pf-name-h" class:err={errs.name}>
      {errs.name ??
        (form.mode === "edit"
          ? "The name can't be changed. Delete and add again to rename."
          : "Letters, digits, - and _. Can't be changed later.")}
    </small>
  </div>

  <div class="field">
    <label for="pf-url">Address (base URL)</label>
    <input
      id="pf-url"
      class="mono"
      bind:value={form.baseURL}
      autocomplete="off"
      spellcheck="false"
      aria-invalid={!!errs.baseURL}
      aria-describedby="pf-url-h"
    />
    <small id="pf-url-h" class:err={errs.baseURL}>
      {errs.baseURL ??
        (cloud
          ? "A cloud service: it is outside this computer."
          : "On this computer.")}
    </small>
  </div>

  <div class="field">
    <label for="pf-key">API key</label>
    <input
      id="pf-key"
      type="password"
      bind:this={keyInput}
      bind:value={form.apiKey}
      autocomplete="new-password"
      spellcheck="false"
      placeholder={form.hasKey ? "•••••• (saved)" : ""}
      aria-invalid={!!errs.apiKey || form.keyNeeded}
      aria-describedby="pf-key-h"
    />
    <small id="pf-key-h" class:err={errs.apiKey || form.keyNeeded}>
      {#if form.keyNeeded}
        You changed the address, so re-enter the API key.
      {:else if errs.apiKey}
        {errs.apiKey}
      {:else if form.hasKey}
        Leave blank to keep the saved key. {#if form.urlChanged}You changed the
          address, so you'll need to enter it again.{/if}
      {:else if form.keyRequired || cloud}
        Stored in your system keyring, never in a file.
      {:else}
        Not needed for a local service.
      {/if}
    </small>
  </div>

  <div class="field">
    <label for="pf-model">Model</label>
    <div class="row">
      {#if form.modelsState === "ready" && !form.manualModel}
        <select
          id="pf-model"
          value={form.model}
          onchange={(e) => form.chooseModel(e.currentTarget.value)}
        >
          {#if form.model && !form.models.includes(form.model)}
            <option value={form.model}>{form.model}</option>
          {/if}
          {#if !form.model}<option value="">Choose a model…</option>{/if}
          {#each form.models as m (m)}<option value={m}>{m}</option>{/each}
          <option value="__manual__">Type a name instead…</option>
        </select>
      {:else}
        <input
          id="pf-model"
          class="mono"
          bind:value={form.model}
          autocomplete="off"
          spellcheck="false"
          placeholder="for example llama3.1"
          aria-invalid={!!errs.model}
        />
      {/if}
      <button
        class="btn"
        type="button"
        disabled={form.busy || form.modelsState === "loading"}
        onclick={() => form.loadModels()}
      >
        {form.modelsState === "loading" ? "Loading…" : "Load models"}
      </button>
    </div>
    <small class:err={errs.model} role="status" aria-live="polite">
      {#if errs.model}
        {errs.model}
      {:else if form.modelsState === "unsupported"}
        This service doesn't list its models. Type the model name.
      {:else if form.modelsState === "error"}
        Couldn't load the list. Type the model name, or check the address and
        key.
      {:else if form.modelsState === "ready"}
        {form.models.length} models found.
      {:else}
        Load models saves your changes first, then asks the service for its
        list.
      {/if}
    </small>
  </div>

  <fieldset class="caps">
    <legend>What this model can do</legend>
    <p class="help">
      Not sure? Leave these off. temail falls back to asking for JSON in the
      prompt.
    </p>
    <label class="check">
      <input type="checkbox" bind:checked={form.structuredOutput} />
      <span>
        Structured output
        <small>The service can force replies into an exact JSON shape.</small>
      </span>
    </label>
    <label class="check">
      <input type="checkbox" bind:checked={form.tools} />
      <span>
        Tools
        <small>The service supports function calling.</small>
      </span>
    </label>
    <div class="field">
      <label for="pf-ctx">Context size (tokens)</label>
      <input
        id="pf-ctx"
        type="number"
        min="0"
        step="1"
        bind:value={form.maxContext}
        aria-invalid={!!errs.maxContext}
        aria-describedby="pf-ctx-h"
      />
      <small id="pf-ctx-h" class:err={errs.maxContext}>
        {errs.maxContext ??
          "How much text it can read at once. 0 means unknown."}
      </small>
    </div>
  </fieldset>

  {#if form.thinkingVisible}
    <div class="field">
      <label for="pf-think">Thinking</label>
      <div class="row">
        <select
          id="pf-think"
          bind:value={form.thinking}
          disabled={form.busy || form.capsState === "loading"}
          aria-describedby="pf-think-h"
        >
          {#each form.thinkingOptions as o (o.value)}
            <option value={o.value}>{o.label}</option>
          {/each}
        </select>
        <button
          class="btn"
          type="button"
          disabled={form.busy || form.capsState === "loading"}
          onclick={() => form.detectThinking()}
        >
          {form.capsState === "loading" ? "Detecting…" : "Detect"}
        </button>
      </div>
      <small id="pf-think-h" role="status" aria-live="polite">
        Reasoning before answering. Off is much faster for labeling; temail uses
        Off for local labeling unless you change it here.
        {#if form.capsState === "ready"}
          Options match this model.
        {:else}
          Support depends on the model.
        {/if}
      </small>
    </div>
  {/if}

  {#if cloud}
    <label class="check">
      <input type="checkbox" bind:checked={form.enabled} />
      <span>
        Turn this provider on
        <small>
          Cloud providers receive email content when you use AI features.
          Nothing is sent until you turn it on. Your system will ask you to
          confirm.
        </small>
      </span>
    </label>
  {/if}

  {#if form.notice}
    <p class="notice" role="status">{form.notice}</p>
  {/if}

  <ErrorBanner error={banner} busy={form.busy} onrecover={recover} />

  <div class="actions">
    <button class="btn" type="button" onclick={oncancel}>Cancel</button>
    <button
      class="btn"
      type="button"
      disabled={form.busy}
      onclick={() => onsubmit(true)}>Save and test</button
    >
    <button class="btn primary" type="submit" disabled={form.busy}
      >{form.busy ? "Saving…" : "Save"}</button
    >
  </div>
</form>

<style>
  .form {
    display: grid;
    gap: 0.9rem;
    padding: 1.25rem 1.4rem;
    background: var(--bg);
    border: 1px solid var(--line);
    border-radius: 10px;
  }
  h2 {
    font-size: 1.1rem;
  }
  h2:focus {
    outline: none;
  }
  label,
  .field {
    display: grid;
    gap: 0.25rem;
    font-weight: 600;
  }
  small {
    font-weight: 400;
    color: var(--fg-dim);
  }
  small.err {
    color: var(--danger);
  }
  .help {
    color: var(--fg-dim);
    font-size: var(--fs-small);
  }
  input:not([type="checkbox"]),
  select {
    padding: 0.45rem 0.6rem;
    background: var(--bg-pane);
    border: 1px solid var(--line);
    border-radius: 5px;
    font-weight: 400;
    min-width: 0;
  }
  input:focus-visible,
  select:focus-visible {
    border-color: var(--accent);
  }
  input[aria-invalid="true"] {
    border-color: var(--danger);
  }
  input:disabled {
    opacity: 0.65;
  }
  .row {
    display: flex;
    gap: 0.5rem;
  }
  .row > :first-child {
    flex: 1;
  }
  fieldset {
    margin: 0;
    padding: 0;
    border: 0;
    display: grid;
    gap: 0.5rem;
  }
  legend {
    padding: 0;
    font-weight: 600;
    margin-bottom: 0.25rem;
  }
  .seg {
    display: inline-flex;
    flex-wrap: wrap;
    gap: 0.35rem;
  }
  .seg button {
    padding: 0.3rem 0.8rem;
    background: var(--bg-raised);
    border: 1px solid var(--line);
    border-radius: 6px;
  }
  .seg button[aria-pressed="true"] {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--accent-fg);
    font-weight: 600;
  }
  .check {
    grid-template-columns: auto 1fr;
    align-items: start;
    gap: 0.6rem;
    font-weight: 400;
  }
  .check > span {
    display: grid;
    font-weight: 600;
  }
  .notice {
    margin: 0;
    padding: 0.5rem 0.75rem;
    border-left: 3px solid var(--warn);
    background: var(--bg-pane);
    border-radius: 4px;
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
  }
</style>
