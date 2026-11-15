<script lang="ts">
  import { onMount } from "svelte";
  import { trapFocus } from "../focus";

  let {
    title,
    body,
    confirmLabel,
    danger = false,
    onconfirm,
    oncancel,
  }: {
    title: string;
    body: string;
    confirmLabel: string;
    danger?: boolean;
    onconfirm: () => void;
    oncancel: () => void;
  } = $props();

  let cancelBtn: HTMLButtonElement;
  let opener: Element | null = null;

  onMount(() => {
    opener = document.activeElement;
    cancelBtn.focus(); // the safe choice has focus first
    return () => (opener as HTMLElement | null)?.focus?.();
  });
</script>

<!-- Esc is handled by the app's key handler (the "back" action). -->
<!-- svelte-ignore a11y_click_events_have_key_events -->
<div class="scrim" role="presentation" onclick={oncancel}>
  <div
    class="card"
    role="alertdialog"
    aria-modal="true"
    aria-labelledby="cd-title"
    aria-describedby="cd-body"
    tabindex="-1"
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
  >
    <h2 id="cd-title">{title}</h2>
    <p id="cd-body">{body}</p>
    <div class="actions">
      <button class="btn" type="button" bind:this={cancelBtn} onclick={oncancel}
        >Cancel</button
      >
      <button class="btn primary" class:danger type="button" onclick={onconfirm}
        >{confirmLabel}</button
      >
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
    z-index: 20;
  }
  .card {
    width: min(26rem, 92vw);
    padding: 1.25rem 1.5rem;
    background: var(--bg-raised);
    border: 1px solid var(--line);
    border-radius: 10px;
    box-shadow: 0 12px 40px #0006;
    display: grid;
    gap: 0.6rem;
  }
  h2 {
    font-size: 1.05rem;
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
    margin-top: 0.5rem;
  }
  .danger {
    background: var(--danger);
    border-color: var(--danger);
    color: var(--bg);
  }
</style>
