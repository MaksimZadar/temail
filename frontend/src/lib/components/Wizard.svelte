<script lang="ts">
  import { onMount, tick } from "svelte";
  import { Wizard, STEPS } from "../setup/wizard.svelte";
  import ErrorBanner from "./ErrorBanner.svelte";
  import { ui } from "../stores/ui.svelte";
  import { app } from "../stores/app.svelte";

  const w = new Wizard(() => app.setupDone());
  let heading = $state<HTMLHeadingElement>();

  onMount(() => {
    void w.start();
    const offs = [
      ui.register("wizard.next", () => w.primary()),
      ui.register("back", () => w.back()),
    ];
    return () => offs.forEach((f) => f());
  });

  // Move focus to the step title whenever the step changes.
  $effect(() => {
    void w.step;
    void tick().then(() => heading?.focus());
  });

  async function copyFp(): Promise<void> {
    if (!w.probe) return;
    try {
      await navigator.clipboard.writeText(w.probe.fingerprint);
      ui.flash("Fingerprint copied");
    } catch {
      ui.flash("Couldn't copy");
    }
  }

  const titles = {
    checks: "Welcome to temail",
    account: "Connect to Bridge",
    cert: "Check this connection",
    test: "Test the connection",
  } as const;
</script>

<main class="wrap">
  <div class="panel">
    <p class="count">Step {w.index + 1} of {STEPS.length}</p>
    <h1 bind:this={heading} tabindex="-1">{titles[w.step]}</h1>

    {#if w.step === "checks"}
      <p class="lead">
        temail reads your Proton Mail through Proton Bridge, which runs on this
        computer. Your mail doesn't go anywhere else.
      </p>
      <ul class="checks" aria-label="Checks">
        <li
          class:ok={w.status?.bridgeReachable}
          class:bad={w.status && !w.status.bridgeReachable}
        >
          <span class="mark" aria-hidden="true"
            >{w.status ? (w.status.bridgeReachable ? "✓" : "!") : "…"}</span
          >
          <span>
            {w.status?.bridgeReachable
              ? "Proton Bridge is running"
              : w.status
                ? "Can't reach Proton Bridge"
                : "Looking for Proton Bridge"}
          </span>
        </li>
        <li
          class:ok={w.status?.keyringOk}
          class:bad={w.status && !w.status.keyringOk}
        >
          <span class="mark" aria-hidden="true"
            >{w.status ? (w.status.keyringOk ? "✓" : "!") : "…"}</span
          >
          <span>
            {w.status?.keyringOk
              ? "Password storage is ready"
              : w.status
                ? "Can't reach the system keyring"
                : "Checking password storage"}
          </span>
        </li>
      </ul>
      {#if w.status && !w.status.bridgeReachable}
        <p class="help">Open Proton Bridge and sign in, then check again.</p>
      {/if}
      {#if w.status && !w.status.keyringOk}
        <p class="help">
          temail keeps your Bridge password in the system keyring, never in a
          file. On KDE, unlock KWallet. On Windows, check Credential Manager.
          {#if w.status.keyringMessage}<br /><code
              >{w.status.keyringMessage}</code
            >{/if}
        </p>
      {/if}
      <div class="actions">
        <button
          class="btn"
          type="button"
          disabled={w.busy}
          onclick={() => w.refreshChecks()}>Check again</button
        >
        <button
          class="btn primary"
          type="button"
          disabled={!w.checksOk || w.busy}
          onclick={() => w.go("account")}>Continue</button
        >
      </div>
    {:else if w.step === "account"}
      <p class="lead">
        Use the username and password Bridge shows under <em>Mailbox details</em
        >. This is not your Proton login password.
      </p>
      <form
        onsubmit={(e) => {
          e.preventDefault();
          void w.submitAccount();
        }}
      >
        <label
          >Bridge username
          <input
            bind:value={w.form.username}
            autocomplete="off"
            spellcheck="false"
            required
          />
        </label>
        <label
          >Bridge password
          <input
            type="password"
            bind:value={w.form.password}
            autocomplete="off"
            required={!w.status?.configured}
          />
          {#if w.status?.configured}<small
              >Leave blank to keep the saved password.</small
            >{/if}
        </label>
        <details>
          <summary>Advanced</summary>
          <div class="grid">
            <label
              >Host
              <input bind:value={w.form.host} spellcheck="false" />
              <small>temail only connects to Bridge on this computer.</small>
            </label>
            <label
              >IMAP port
              <input
                type="number"
                min="1"
                max="65535"
                bind:value={w.form.imapPort}
              />
            </label>
            <label
              >SMTP port
              <input
                type="number"
                min="1"
                max="65535"
                bind:value={w.form.smtpPort}
              />
            </label>
            <label
              >Connection type
              <select bind:value={w.form.tlsMode}>
                <option value="starttls">STARTTLS (Bridge default)</option>
                <option value="ssl">SSL/TLS</option>
              </select>
            </label>
          </div>
        </details>
        <div class="actions">
          <button
            class="btn"
            type="button"
            disabled={w.busy}
            onclick={() => w.back()}>Back</button
          >
          <button class="btn primary" type="submit" disabled={w.busy}
            >{w.busy ? "Saving…" : "Continue"}</button
          >
        </div>
      </form>
    {:else if w.step === "cert"}
      <p class="lead">
        Bridge makes its own security certificate, so your computer can't vouch
        for it. temail will remember this one and refuse to talk to anything
        else.
      </p>
      {#if w.probe}
        <div class="fp">
          <span class="label">Fingerprint</span>
          <code class="mono">{w.probe.fingerprint}</code>
          {#if w.probe.subject}<span class="label"
              >Issued to <span class="mono">{w.probe.subject}</span></span
            >{/if}
          <button class="btn" type="button" onclick={copyFp}>Copy</button>
        </div>
        <p class="help">
          The fingerprint is like a name tag for the certificate. In Bridge,
          open Settings and export the TLS certificate if you want to compare.
          If you didn't expect to see this, or it changes later, stop and find
          out why.
        </p>
      {:else if w.busy}
        <p class="help">Asking Bridge for its certificate…</p>
      {/if}
      <div class="actions">
        <button
          class="btn"
          type="button"
          disabled={w.busy}
          onclick={() => w.back()}>Back</button
        >
        {#if w.trustFailure === "already_pinned"}
          <button
            class="btn primary"
            type="button"
            disabled={w.busy || !w.probe}
            onclick={() => w.trust(true)}
          >
            Replace saved certificate
          </button>
        {:else}
          <button
            class="btn primary"
            type="button"
            disabled={w.busy || !w.probe}
            onclick={() => w.trust(false)}
          >
            Trust this certificate
          </button>
        {/if}
      </div>
    {:else}
      {#if w.tested}
        <p class="lead ok-text">Connected. Your mail will start loading now.</p>
        <div class="actions">
          <button class="btn primary" type="button" onclick={() => w.finish()}
            >Open mailbox</button
          >
        </div>
      {:else if w.busy}
        <p class="lead">Connecting to Bridge…</p>
      {/if}
      {#if !w.tested && !w.busy && !w.error}
        <div class="actions">
          <button class="btn" type="button" onclick={() => w.runTest()}
            >Test again</button
          >
        </div>
      {/if}
    {/if}

    <ErrorBanner error={w.error} busy={w.busy} onrecover={() => w.recover()} />
    {#if app.loadError}
      <p class="help">The setup state couldn't be read, so setup is shown.</p>
    {/if}
  </div>
  <p class="keys">
    Enter continues. Esc goes back. Press <kbd>?</kbd> for shortcuts.
  </p>
</main>

<style>
  .wrap {
    display: grid;
    align-content: center;
    justify-items: center;
    gap: 1rem;
    height: 100%;
    padding: 1.5rem;
    overflow: auto;
  }
  .panel {
    width: min(30rem, 100%);
    padding: 1.75rem 2rem;
    background: var(--bg-raised);
    border: 1px solid var(--line);
    border-radius: 12px;
    box-shadow: 0 8px 30px #0002;
  }
  .count {
    color: var(--accent);
    font-size: var(--fs-small);
    font-weight: 600;
  }
  h1 {
    margin: 0.15rem 0 0.75rem;
    font-size: 1.4rem;
  }
  h1:focus {
    outline: none;
  }
  .lead {
    margin-bottom: 1rem;
    font-size: var(--fs-body);
  }
  .ok-text {
    color: var(--ok);
  }
  .help {
    margin-top: 0.75rem;
    color: var(--fg-dim);
  }
  .checks {
    margin: 0;
    padding: 0;
    list-style: none;
    display: grid;
    gap: 0.4rem;
  }
  .checks li {
    display: flex;
    gap: 0.6rem;
    align-items: center;
  }
  .mark {
    display: inline-grid;
    place-items: center;
    width: 1.3rem;
    height: 1.3rem;
    border-radius: 50%;
    background: var(--line);
    font-weight: 700;
    font-size: var(--fs-small);
  }
  .ok .mark {
    background: var(--ok);
    color: var(--bg);
  }
  .bad .mark {
    background: var(--danger);
    color: var(--bg);
  }
  form {
    display: grid;
    gap: 0.9rem;
  }
  label {
    display: grid;
    gap: 0.25rem;
    font-weight: 600;
  }
  small {
    font-weight: 400;
    color: var(--fg-dim);
  }
  input,
  select {
    padding: 0.45rem 0.6rem;
    background: var(--bg);
    border: 1px solid var(--line);
    border-radius: 5px;
    font-weight: 400;
  }
  input:focus-visible,
  select:focus-visible {
    border-color: var(--accent);
  }
  summary {
    cursor: pointer;
    color: var(--fg-dim);
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.75rem;
    margin-top: 0.75rem;
  }
  .grid label:first-child {
    grid-column: 1 / -1;
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
    margin-top: 1.25rem;
  }
  .fp {
    display: grid;
    gap: 0.35rem;
    justify-items: start;
    padding: 0.75rem 1rem;
    background: var(--bg);
    border: 1px solid var(--line);
    border-radius: 6px;
  }
  .fp code {
    overflow-wrap: anywhere;
    font-size: var(--fs-ui);
  }
  .label {
    color: var(--fg-dim);
    font-size: var(--fs-small);
  }
  .keys {
    color: var(--fg-dim);
    font-size: var(--fs-small);
  }
</style>
