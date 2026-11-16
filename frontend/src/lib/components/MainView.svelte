<script lang="ts">
  import { onMount } from "svelte";
  import * as api from "../api";
  import { mail } from "../stores/mail.svelte";
  import { layout, MIN, MAX } from "../stores/layout.svelte";
  import { sync } from "../stores/sync.svelte";
  import { cache } from "../stores/cache.svelte";
  import { ui } from "../stores/ui.svelte";
  import { sel } from "../stores/select.svelte";
  import { actions } from "../stores/actions.svelte";
  import { findByKind } from "../mail/folders";
  import type { MailboxKind } from "../api";
  import Toolbar from "./Toolbar.svelte";
  import FolderPane from "./FolderPane.svelte";
  import MessageList from "./MessageList.svelte";
  import MessagePane from "./MessagePane.svelte";
  import Resizer from "./Resizer.svelte";

  let filterInput = $state<HTMLInputElement>();
  let view = $state<ReturnType<typeof MessagePane>>();

  const rowsPerPage = (): number =>
    Math.max(5, Math.floor((window.innerHeight - 120) / 28));

  function gotoKind(kind: MailboxKind, label: string): void {
    const f = findByKind(mail.folders, kind);
    if (!f) return ui.flash(`No ${label} folder`);
    layout.setFocus("list");
    void mail.selectFolder(f.name);
  }

  type Unit = "line" | "half" | "page";
  /** Move by rows, half pages or pages. In the message pane this scrolls it. */
  function move(
    delta: 1 | -1,
    count: number | undefined,
    unit: Unit = "line",
  ): void {
    const c = count ?? 1;
    if (layout.focus === "view") {
      void view?.scrollView(unit, delta, c);
      return;
    }
    const rows =
      unit === "line"
        ? 1
        : unit === "half"
          ? Math.floor(rowsPerPage() / 2)
          : rowsPerPage();
    if (layout.focus === "folders") mail.moveFolder(delta * rows * c);
    else mail.moveCursor(delta * rows * c);
  }
  function jump(to: "first" | "last", count: number | undefined): void {
    if (layout.focus === "folders") {
      const i = to === "first" ? 0 : mail.folders.length - 1;
      mail.folderCursor = mail.folders[i]?.name ?? null;
    } else if (layout.focus === "view") {
      void view?.scrollView(to === "first" ? "top" : "bottom", 1);
    } else if (layout.focus === "list") {
      if (count !== undefined) mail.cursorTo(count - 1);
      else if (to === "first") mail.cursorTo(0);
      else void mail.cursorToEnd();
    }
  }
  function open(): void {
    if (layout.focus === "folders") {
      const f = mail.folders.find((x) => x.name === mail.folderCursor);
      if (f) {
        layout.setFocus("list");
        void mail.selectFolder(f.name);
      }
    } else if (layout.focus === "list") {
      layout.setFocus("view");
      void mail.openCursor();
    } else view?.focusBody();
  }
  function right(): void {
    if (layout.focus === "view") view?.focusBody();
    else if (layout.focus === "folders") open();
    else open();
  }
  function left(): void {
    layout.step(-1);
  }

  /** Actions ignore count prefixes: "5e" is just "e", with a hint. */
  const act = (count: number | undefined, fn: () => void): void => {
    if (count !== undefined) ui.flash("Counts don't apply to actions");
    fn();
  };
  const withCursor = (fn: () => void): void => {
    // Selecting works in the list; from the folder pane there is no row.
    if (layout.focus === "folders") return ui.flash("Pick a message first");
    fn();
  };

  onMount(() => {
    void sync.start();
    cache.start();
    void mail.loadFolders();

    // Refresh from sync events, coalesced: at most one reload every 2 s while
    // syncing, and one shortly after it ends.
    let timer: ReturnType<typeof setTimeout> | undefined;
    let due = 0;
    const schedule = (ms: number): void => {
      const at = Date.now() + ms;
      if (timer !== undefined && due <= at) return;
      clearTimeout(timer);
      mail.stopLive();
      due = at;
      timer = setTimeout(() => {
        timer = undefined;
        void mail.refreshHead();
      }, ms);
    };
    const offs = [
      api.onEvent("sync:progress", () => schedule(2000)),
      api.onEvent("sync:done", () => schedule(300)),
      // Live changes: coalesced in the store, which updates the counts and the
      // open list without moving the cursor.
      api.onEvent<{ mailboxes: string[] }>("sync:changed", (e) =>
        mail.noteChanged(e?.mailboxes),
      ),
      api.onEvent("actions:done", () => actions.noteDone()),
      ui.register("msg.archive", (c) =>
        act(c, () => void actions.run("archive")),
      ),
      ui.register("msg.trash", (c) => act(c, () => void actions.run("trash"))),
      ui.register("msg.spam", (c) =>
        act(
          c,
          () =>
            void actions.run(
              mail.folder?.kind === "spam" ? "not_spam" : "spam",
            ),
        ),
      ),
      ui.register("msg.move", (c) => act(c, () => actions.openPicker("move"))),
      ui.register("msg.label", (c) =>
        act(c, () => actions.openPicker("label")),
      ),
      ui.register("msg.read", (c) => act(c, () => void actions.toggleRead())),
      ui.register("msg.star", (c) => act(c, () => void actions.toggleStar())),
      ui.register("msg.undo", (c) => act(c, () => void actions.undoLast())),
      ui.register("select.toggle", (c) =>
        act(c, () => withCursor(() => sel.toggle())),
      ),
      ui.register("select.visual", (c) =>
        act(c, () => withCursor(() => sel.visualToggle())),
      ),
      ui.register("activity.open", () => (ui.overlay = "activity")),
      ui.register("list.down", (c) => move(1, c)),
      ui.register("list.up", (c) => move(-1, c)),
      ui.register("list.first", (c) => jump("first", c)),
      ui.register("list.last", (c) => jump("last", c)),
      ui.register("list.halfDown", (c) => move(1, c, "half")),
      ui.register("list.halfUp", (c) => move(-1, c, "half")),
      ui.register("list.pageDown", (c) => move(1, c, "page")),
      ui.register("list.pageUp", (c) => move(-1, c, "page")),
      ui.register("pane.left", left),
      ui.register("pane.right", right),
      ui.register("folders.toggle", () => layout.toggleFolders()),
      ui.register("open", open),
      ui.register("message.next", () => void mail.step(1)),
      ui.register("message.prev", () => void mail.step(-1)),
      ui.register("search.filter", () => filterInput?.focus()),
      ui.register("goto.inbox", () => gotoKind("inbox", "Inbox")),
      ui.register("goto.sent", () => gotoKind("sent", "Sent")),
      ui.register("goto.drafts", () => gotoKind("drafts", "Drafts")),
      ui.register("goto.trash", () => gotoKind("trash", "Trash")),
      ui.register("goto.archive", () => gotoKind("archive", "Archive")),
      ui.register("goto.spam", () => gotoKind("spam", "Spam")),
      ui.register("goto.pick", () => {
        ui.paletteQuery = "Go to ";
        ui.overlay = "palette";
      }),
      ui.register("settings.open", () => (ui.screen = "settings")),
      ui.register("sync.refresh", () => void sync.syncNow()),
      ui.register("view.plain", () => (mail.plain = !mail.plain)),
      ui.register("view.links", () => view?.toggleLinks()),
      ui.register("copy.sender", () => {
        const a = mail.message?.from?.[0]?.email;
        if (!a) return ui.flash("Open a message first");
        navigator.clipboard
          .writeText(a)
          .then(() => ui.flash("Sender address copied"))
          .catch(() => ui.flash("Couldn't copy"));
      }),
      ui.register("back", () => {
        if (view?.closeLinks()) return;
        if (sel.count > 0) sel.clear();
        else if (mail.filter) mail.setFilter("");
        else if (layout.focus !== "list") layout.setFocus("list");
      }),
    ];
    const onResize = (): void => layout.setWidth(window.innerWidth);
    window.addEventListener("resize", onResize);
    onResize();
    return () => {
      clearTimeout(timer);
      window.removeEventListener("resize", onResize);
      offs.forEach((f) => f());
      sync.stop();
      actions.stop();
      cache.stop();
    };
  });
</script>

<div class="main">
  <Toolbar bind:filterInput />
  <div class="panes">
    {#if layout.visible.includes("folders")}
      <div
        class="slot"
        style:width={layout.narrow ? "100%" : `${layout.folderW}px`}
      >
        <FolderPane />
      </div>
      {#if !layout.narrow}
        <Resizer
          label="Resize folders"
          value={layout.folderW}
          min={MIN.folders}
          max={MAX.folders}
          onchange={(w) => layout.setFolderW(w)}
        />
      {/if}
    {/if}
    {#if layout.visible.includes("list")}
      <div
        class="slot"
        class:flex={layout.narrow}
        style:width={layout.narrow ? "100%" : `${layout.listW}px`}
      >
        <MessageList />
      </div>
      {#if !layout.narrow}
        <Resizer
          label="Resize message list"
          value={layout.listW}
          min={MIN.list}
          max={MAX.list}
          onchange={(w) => layout.setListW(w)}
        />
      {/if}
    {/if}
    {#if layout.visible.includes("view")}
      <div class="slot flex">
        <MessagePane bind:this={view} />
      </div>
    {/if}
  </div>
</div>

<style>
  .main {
    display: flex;
    flex-direction: column;
    height: 100%;
    background: var(--bg-pane);
  }
  /* Panes are separate cards: the same 8px margin on every side and between them
     (the resize handles sit in the gaps). */
  .panes {
    flex: 1;
    min-height: 0;
    display: flex;
    padding: 8px;
  }
  .slot {
    flex: none;
    min-width: 0;
    height: 100%;
  }
  .slot.flex {
    flex: 1;
  }
</style>
