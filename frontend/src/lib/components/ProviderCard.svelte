<script lang="ts">
  import type { LLMProvider } from "../api";
  import { testCopy } from "../llm/errors";
  import type { TestState } from "../llm/store.svelte";

  let {
    provider: p,
    test,
    ontest,
    onedit,
    ondefault,
    ondelete,
    ontoggle,
    onrecover,
  }: {
    provider: LLMProvider;
    test?: TestState;
    ontest: () => void;
    onedit: () => void;
    ondefault: () => void;
    ondelete: () => void;
    ontoggle: (enabled: boolean) => void;
    onrecover: (action: "edit" | "enable" | "retry" | "key") => void;
  } = $props();

  const cloud = $derived(p.kind === "cloud");
  const result = $derived(test && !test.busy ? test.result : null);
  const copy = $derived(result ? testCopy(result.category) : null);
</script>

<li class="card" class:off={cloud && !p.enabled} aria-label={p.name}>
  <div class="top">
    <h3>{p.name}</h3>
    <span class="badge" class:cloud>{cloud ? "Cloud" : "Local"}</span>
    {#if p.isDefault}<span class="badge def">Default</span>{/if}
    {#if !cloud}
      <span class="badge" data-testid="thinking-badge"
        >Thinking: {p.thinking === "" ? "default" : p.thinking === "none" ? "off" : p.thinking}</span
      >
    {/if}
    <span class="grow"></span>
    {#if cloud}
      <label class="switch">
        <input
          type="checkbox"
          role="switch"
          checked={p.enabled}
          aria-label="Use {p.name}"
          onchange={(e) => {
            // The parent decides (it may ask first). Keep the box in sync.
            const want = e.currentTarget.checked;
            e.currentTarget.checked = p.enabled;
            ontoggle(want);
          }}
        />
        <span>{p.enabled ? "On" : "Off"}</span>
      </label>
    {/if}
  </div>
  <dl>
    <dt>Model</dt>
    <dd>{p.model || "Not chosen yet"}</dd>
    <dt>Address</dt>
    <dd class="mono">{p.baseURL}</dd>
    <dt>API key</dt>
    <dd>{p.hasKey ? "Saved" : cloud ? "Missing" : "Not needed"}</dd>
  </dl>
  {#if cloud && !p.enabled}
    <p class="note">Turned off. Nothing is sent to it.</p>
  {/if}
  <div class="actions">
    <button class="btn" type="button" disabled={test?.busy} onclick={ontest}
      >{test?.busy ? "Testing…" : "Test"}</button
    >
    <button class="btn" type="button" onclick={onedit}>Edit</button>
    {#if !p.isDefault}
      <button class="btn" type="button" onclick={ondefault}>Set default</button>
    {/if}
    <span class="grow"></span>
    <button class="btn del" type="button" onclick={ondelete}>Delete</button>
  </div>
  <div class="result" role="status" aria-live="polite">
    {#if result && copy}
      <div class="out" class:ok={result.ok} class:bad={!result.ok}>
        <strong>{copy.title}</strong>
        <span>{copy.hint}</span>
        {#if !result.ok && copy.action}
          <button
            class="btn small"
            type="button"
            onclick={() => onrecover(copy.action!)}>{copy.actionLabel}</button
          >
        {/if}
      </div>
    {/if}
  </div>
</li>

<style>
  .card {
    list-style: none;
    padding: 0.9rem 1.1rem;
    background: var(--bg);
    border: 1px solid var(--line);
    border-radius: 8px;
    display: grid;
    gap: 0.5rem;
  }
  .top {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  h3 {
    font-size: var(--fs-body);
  }
  .grow {
    flex: 1;
  }
  .badge {
    padding: 0.05rem 0.5rem;
    border: 1px solid var(--line);
    border-radius: 999px;
    font-size: var(--fs-small);
    color: var(--fg-dim);
  }
  .badge.cloud {
    color: var(--warn);
    border-color: currentColor;
  }
  .badge.def {
    color: var(--accent);
    border-color: var(--accent);
    font-weight: 600;
  }
  dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 0.1rem 0.9rem;
    margin: 0;
  }
  dt {
    color: var(--fg-dim);
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .note {
    color: var(--fg-dim);
    font-size: var(--fs-small);
  }
  .off dl {
    opacity: 0.8;
  }
  .switch {
    display: inline-flex;
    gap: 0.4rem;
    align-items: center;
    cursor: pointer;
  }
  .actions {
    display: flex;
    gap: 0.4rem;
    flex-wrap: wrap;
  }
  .small {
    padding: 0.15rem 0.6rem;
  }
  .del:hover:not(:disabled) {
    border-color: var(--danger);
    color: var(--danger);
  }
  .out {
    display: grid;
    gap: 0.2rem;
    justify-items: start;
    padding: 0.5rem 0.7rem;
    border-left: 3px solid var(--line);
    border-radius: 4px;
    background: var(--bg-pane);
  }
  .out.ok {
    border-left-color: var(--ok);
  }
  .out.bad {
    border-left-color: var(--danger);
  }
  .out.ok strong {
    color: var(--ok);
  }
  .out.bad strong {
    color: var(--danger);
  }
</style>
