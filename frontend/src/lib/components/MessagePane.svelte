<script lang="ts">
  import { onMount, tick } from "svelte";
  import { mail } from "../stores/mail.svelte";
  import { layout } from "../stores/layout.svelte";
  import { ui } from "../stores/ui.svelte";
  import * as api from "../api";
  import {
    addressText,
    formatFullDate,
    formatSize,
    senderName,
  } from "../mail/format";
  import { buildSrcdoc, htmlToText, prepareHtml } from "../mail/render";
  import { needsConfirm, type MailLink } from "../mail/links";

  const focused = $derived(layout.focus === "view");
  const m = $derived(mail.message);
  const hasHtml = $derived(!!m?.htmlBody?.trim());
  const hasText = $derived(!!m?.textBody?.trim());
  // Plain text is shown when asked for, or when there is no HTML part.
  const showPlain = $derived(!!m && (mail.plain || !hasHtml));
  const prepared = $derived(m && hasHtml ? prepareHtml(m.htmlBody) : null);
  const srcdoc = $derived(prepared ? buildSrcdoc(prepared.html) : "");
  const plainText = $derived(
    !m ? "" : hasText ? m.textBody : hasHtml ? htmlToText(m.htmlBody) : "",
  );
  const links = $derived(prepared?.links ?? []);

  let frame = $state<HTMLIFrameElement>();
  let bodyEl = $state<HTMLDivElement>();
  /** True while the keyboard is inside the message frame (a separate document). */
  let frameFocused = $state(false);
  let pane = $state<HTMLElement>();
  let showLinks = $state(false);
  let pendingLink = $state<MailLink | null>(null);
  let attError = $state<Record<string, string>>({});
  let attBusy = $state<Record<string, boolean>>({});

  // Reset per-message UI when another message opens.
  $effect(() => {
    void mail.openKey;
    showLinks = false;
    pendingLink = null;
    attError = {};
  });

  /**
   * Put keyboard focus on the message pane. It goes to a host element, never
   * into the frame: keys typed inside a frame never reach the app, so h, Esc
   * and friends would stop working.
   */
  export function focusBody(): void {
    frameFocused = false;
    if (showPlain) pane?.querySelector<HTMLElement>(".plain")?.focus();
    else bodyEl?.focus();
  }

  /** Give the keys back to the app after the frame took them (click or Tab). */
  export function releaseFrame(): void {
    frameFocused = false;
    (frame as HTMLElement | undefined)?.blur();
    bodyEl?.focus();
  }

  type Step = "line" | "half" | "page" | "top" | "bottom";

  /**
   * Scroll the message from the keyboard. A sandboxed frame can't be scrolled
   * by the app (no same-origin, no scripts), so the formatted view switches to
   * plain text, which the app scrolls itself. `g h` switches back.
   */
  export async function scrollView(
    step: Step,
    dir: 1 | -1,
    count = 1,
  ): Promise<void> {
    if (!m) return;
    if (!showPlain) {
      mail.plain = true;
      ui.flash("Plain text, so the keys can scroll. g h switches back.");
      await tick();
    }
    const el = pane?.querySelector<HTMLElement>(".plain");
    if (!el) return;
    const h = el.clientHeight || 400;
    if (step === "top") el.scrollTop = 0;
    else if (step === "bottom") el.scrollTop = el.scrollHeight;
    else {
      const unit = step === "line" ? 48 : step === "half" ? h / 2 : h * 0.9;
      el.scrollBy({ top: dir * unit * count });
    }
  }

  // The frame owns the keyboard once it is clicked. Notice that and offer a way out.
  onMount(() => {
    const check = (): void => {
      frameFocused = !!frame && document.activeElement === frame;
    };
    const onBlur = (): void => void setTimeout(check, 0);
    window.addEventListener("blur", onBlur);
    window.addEventListener("focus", check);
    document.addEventListener("focusin", check);
    return () => {
      window.removeEventListener("blur", onBlur);
      window.removeEventListener("focus", check);
      document.removeEventListener("focusin", check);
    };
  });

  /** Close the links list or the open-link prompt. True if something closed. */
  export function closeLinks(): boolean {
    if (pendingLink) {
      pendingLink = null;
      return true;
    }
    if (showLinks) {
      showLinks = false;
      return true;
    }
    return false;
  }

  export function toggleLinks(): void {
    if (links.length > 0) showLinks = !showLinks;
    else ui.flash("No links in this message");
  }

  async function openLink(l: MailLink): Promise<void> {
    if (needsConfirm(l.text, l.href)) {
      pendingLink = l;
      return;
    }
    await go(l.href);
  }
  async function go(href: string): Promise<void> {
    pendingLink = null;
    try {
      await api.openUrl(href);
    } catch {
      ui.flash("Couldn't open the link");
    }
  }

  async function download(a: api.Attachment): Promise<void> {
    if (!m) return;
    attBusy = { ...attBusy, [a.partId]: true };
    attError = { ...attError, [a.partId]: "" };
    try {
      const d = await api.getAttachment(
        m.mailbox,
        m.uidValidity,
        m.uid,
        a.partId,
      );
      const bytes = Uint8Array.from(atob(d.data), (c) => c.charCodeAt(0));
      const url = URL.createObjectURL(
        new Blob([bytes], {
          type: d.contentType || "application/octet-stream",
        }),
      );
      const link = document.createElement("a");
      link.href = url;
      link.download = (d.filename || a.filename || "attachment").replace(
        /[\\/]/g,
        "_",
      );
      document.body.append(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 10_000);
    } catch (e) {
      attError = {
        ...attError,
        [a.partId]: /too large|25|limit/i.test(String(e))
          ? "Too large to download here."
          : "Couldn't download this file.",
      };
    } finally {
      attBusy = { ...attBusy, [a.partId]: false };
    }
  }
</script>

<section
  class="pane"
  class:focused
  data-focused={focused}
  aria-label="Message"
  data-pane="view"
  bind:this={pane}
  onfocusin={() => (layout.focus = "view")}
  onpointerdown={() => (layout.focus = "view")}
>
  {#if mail.messageLoading}
    <p class="state" role="status">Opening message…</p>
  {:else if mail.messageError}
    <div class="state">
      <p class="err" role="alert">{mail.messageError}</p>
      <button class="btn" type="button" onclick={() => mail.openCursor()}
        >Try again</button
      >
    </div>
  {:else if !m}
    <p class="state">Select a message to read it.</p>
  {:else}
    <header>
      <h1>{m.subject || "(no subject)"}</h1>
      <dl>
        <dt>From</dt>
        <dd>
          <strong>{senderName(m.from)}</strong>
          <span class="addr">{addressText(m.from)}</span>
        </dd>
        <dt>To</dt>
        <dd>{addressText(m.to) || "(none)"}</dd>
        <dt>Date</dt>
        <dd>{formatFullDate(m.date)}</dd>
      </dl>
      <div class="bar">
        {#if hasHtml && hasText}
          <div class="seg" role="group" aria-label="Message format">
            <button
              type="button"
              aria-pressed={!showPlain}
              onclick={() => (mail.plain = false)}
              title="Formatted (g h)">Formatted</button
            >
            <button
              type="button"
              aria-pressed={showPlain}
              onclick={() => (mail.plain = true)}
              title="Plain text (g h)">Plain text</button
            >
          </div>
        {/if}
        {#if links.length > 0}
          <button
            class="btn small"
            type="button"
            aria-expanded={showLinks}
            onclick={toggleLinks}
            title="Links in this message (g x)"
          >
            Links ({links.length})
          </button>
        {/if}
      </div>
    </header>

    {#if hasHtml && !showPlain}
      <p class="info" role="note">Remote images are blocked.</p>
    {/if}

    {#if showLinks}
      <div class="links" role="region" aria-label="Links in this message">
        <ul>
          {#each links as l (l.href + l.text)}
            <li>
              <button
                type="button"
                onclick={() => openLink(l)}
                title="Open in your browser"
              >
                <span class="lt">{l.text || l.href}</span>
                <span class="lh mono">{l.href}</span>
              </button>
            </li>
          {/each}
        </ul>
      </div>
    {/if}
    {#if pendingLink}
      <div class="confirm" role="alertdialog" aria-label="Open this link?">
        <p>
          This link says <strong>{pendingLink.text}</strong> but goes to
          <span class="mono">{pendingLink.href}</span>. Open it anyway?
        </p>
        <div class="row">
          <button class="btn" type="button" onclick={() => (pendingLink = null)}
            >Cancel</button
          >
          <button
            class="btn primary"
            type="button"
            onclick={() => go(pendingLink!.href)}>Open in browser</button
          >
        </div>
      </div>
    {/if}

    {#if (m.attachments ?? []).length > 0}
      <ul class="atts" aria-label="Attachments">
        {#each m.attachments ?? [] as a (a.partId)}
          <li class="chip">
            <span class="an">{a.filename || "(unnamed)"}</span>
            <span class="meta">{a.contentType} · {formatSize(a.size)}</span>
            <button
              class="btn small"
              type="button"
              disabled={attBusy[a.partId]}
              aria-label="Download {a.filename || 'attachment'}"
              onclick={() => download(a)}
              >{attBusy[a.partId] ? "Downloading…" : "Download"}</button
            >
            {#if attError[a.partId]}<span class="err" role="alert"
                >{attError[a.partId]}</span
              >{/if}
          </li>
        {/each}
      </ul>
    {/if}

    {#if frameFocused}
      <div class="trap" role="status">
        <span
          >The message has your keyboard. Click outside it or press Tab to get
          your keys back.</span
        >
        <button class="btn small" type="button" onclick={releaseFrame}
          >Back to keys</button
        >
      </div>
    {/if}

    <div class="body" tabindex="-1" bind:this={bodyEl}>
      {#if showPlain}
        <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
        <pre class="plain" tabindex="0" aria-label="Message text">{plainText ||
            "(no text)"}</pre>
      {:else}
        <iframe
          bind:this={frame}
          title="Message body"
          tabindex="-1"
          sandbox=""
          referrerpolicy="no-referrer"
          {srcdoc}
        ></iframe>
      {/if}
    </div>
  {/if}
</section>

<style>
  .pane {
    display: flex;
    flex-direction: column;
    min-width: 0;
    height: 100%;
    background: var(--bg);
    border: 1px solid var(--line);
    border-radius: 8px;
    overflow: hidden;
    transition:
      border-color 0.12s,
      box-shadow 0.12s;
  }
  .pane.focused {
    border-color: var(--accent);
    box-shadow:
      0 0 0 1px var(--accent),
      0 0 0 4px var(--accent-soft);
  }
  @media (forced-colors: active) {
    .pane.focused {
      border-color: Highlight;
      outline: 2px solid Highlight;
      outline-offset: -1px;
    }
  }
  .state {
    margin: auto;
    padding: 1rem;
    color: var(--fg-dim);
    text-align: center;
    display: grid;
    gap: 0.5rem;
    justify-items: center;
  }
  .err {
    color: var(--danger);
  }
  header {
    padding: 0.75rem 1rem 0.5rem;
    border-bottom: 1px solid var(--line);
  }
  h1 {
    font-size: 1.1rem;
    line-height: 1.3;
    overflow-wrap: anywhere;
  }
  dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 0.1rem 0.75rem;
    margin: 0.4rem 0;
  }
  dt {
    color: var(--fg-dim);
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .addr {
    color: var(--fg-dim);
  }
  .bar {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    min-height: 1.5rem;
  }
  .seg {
    display: inline-flex;
    border: 1px solid var(--line);
    border-radius: 5px;
    overflow: hidden;
  }
  .seg button {
    padding: 0.15rem 0.7rem;
    background: var(--bg-raised);
    border: 0;
  }
  .seg button[aria-pressed="true"] {
    background: var(--accent);
    color: var(--accent-fg);
    font-weight: 600;
  }
  .small {
    padding: 0.15rem 0.6rem;
  }
  .info {
    margin: 0;
    padding: 0.3rem 1rem;
    font-size: var(--fs-small);
    color: var(--fg-dim);
    background: var(--accent-soft);
  }
  .links,
  .confirm {
    padding: 0.5rem 1rem;
    border-bottom: 1px solid var(--line);
    background: var(--bg-raised);
    max-height: 12rem;
    overflow: auto;
  }
  .links ul {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .links button {
    display: grid;
    width: 100%;
    text-align: left;
    padding: 0.25rem 0.4rem;
    background: none;
    border: 0;
    border-radius: 4px;
  }
  .links button:hover {
    background: var(--sel-bg);
  }
  .lh {
    color: var(--fg-dim);
    overflow-wrap: anywhere;
  }
  .confirm .row {
    display: flex;
    gap: 0.5rem;
    justify-content: flex-end;
    margin-top: 0.5rem;
  }
  .atts {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    margin: 0;
    padding: 0.5rem 1rem;
    list-style: none;
    border-bottom: 1px solid var(--line);
  }
  .chip {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    padding: 0.2rem 0.3rem 0.2rem 0.6rem;
    background: var(--bg-raised);
    border: 1px solid var(--line);
    border-radius: 6px;
  }
  .an {
    font-weight: 600;
  }
  .meta {
    color: var(--fg-dim);
    font-size: var(--fs-small);
  }
  .body {
    flex: 1;
    min-height: 0;
    display: flex;
  }
  .body:focus {
    outline: none;
  }
  .trap {
    display: flex;
    gap: 0.75rem;
    align-items: center;
    justify-content: space-between;
    padding: 0.35rem 1rem;
    background: var(--accent-soft);
    border-bottom: 1px solid var(--line);
  }
  iframe {
    flex: 1;
    width: 100%;
    border: 0;
    background: #fff;
  }
  iframe:focus-visible {
    outline: none;
  }
  .plain {
    flex: 1;
    margin: 0;
    padding: 1rem;
    overflow: auto;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font: 400 var(--fs-body) / 1.55 var(--font-ui);
    background: var(--bg-raised);
  }
</style>
