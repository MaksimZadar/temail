<script lang="ts">
  import { onMount } from "svelte";
  import { app } from "./lib/stores/app.svelte";
  import { ui } from "./lib/stores/ui.svelte";
  import { Dispatcher } from "./lib/keys/dispatcher";
  import Wizard from "./lib/components/Wizard.svelte";
  import MainPlaceholder from "./lib/components/MainPlaceholder.svelte";
  import Footer from "./lib/components/Footer.svelte";
  import HelpOverlay from "./lib/components/HelpOverlay.svelte";
  import Palette from "./lib/components/Palette.svelte";
  import WhichKey from "./lib/components/WhichKey.svelte";

  $effect(() => {
    ui.context = app.route === "main" ? "main" : "wizard";
  });

  onMount(() => {
    const d = new Dispatcher({
      contexts: () => [ui.context, "global"],
      run: (b, count) => ui.run(b, count),
      overlayOpen: () => ui.overlay !== "none",
      closeOverlay: () => (ui.overlay = "none"),
      onPendingChange: (p, c) => ui.setPending(p, c),
      onHint: (t) => ui.flash(t),
    });
    const offs = [
      ui.register(
        "help.toggle",
        () => (ui.overlay = ui.overlay === "help" ? "none" : "help"),
      ),
      ui.register("palette.open", () => (ui.overlay = "palette")),
    ];
    const onKey = (e: KeyboardEvent): void => void d.handle(e);
    window.addEventListener("keydown", onKey);
    void app.load();
    return () => {
      window.removeEventListener("keydown", onKey);
      offs.forEach((f) => f());
    };
  });
</script>

<div class="shell">
  <div class="content">
    {#if app.route === "setup"}
      <Wizard />
    {:else if app.route === "main"}
      <MainPlaceholder />
    {:else}
      <p class="loading" role="status">Loading…</p>
    {/if}
  </div>
  <Footer />
</div>
{#if ui.overlay === "help"}<HelpOverlay />{/if}
{#if ui.overlay === "palette"}<Palette />{/if}
<WhichKey />

<style>
  .shell {
    display: flex;
    flex-direction: column;
    height: 100%;
  }
  .content {
    flex: 1;
    min-height: 0;
  }
  .loading {
    display: grid;
    place-items: center;
    height: 100%;
    color: var(--fg-dim);
  }
</style>
