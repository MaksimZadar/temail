// The one keymap table. The dispatcher, the help overlay and the which-key
// popup are all generated from it. Key tokens: a printable key as typed
// ("j", "G", "?"), or "C-d" for Ctrl+d, "Enter", "Escape", "Tab".

export type Context = "global" | "wizard" | "main";
export type Phase = 2 | 3 | 4 | 5;

export interface Binding {
  keys: string[];
  action: string;
  context: Context;
  /** Phase that implements it. Anything above 2 is reserved ("later"). */
  phase: Phase;
  desc: string;
  group: string;
}

const b = (
  keys: string,
  action: string,
  context: Context,
  phase: Phase,
  group: string,
  desc: string,
): Binding => ({ keys: keys.split(" "), action, context, phase, group, desc });

export const keymap: Binding[] = [
  // Everywhere
  b("?", "help.toggle", "global", 2, "General", "Show keys"),
  b(":", "palette.open", "global", 2, "General", "Command palette"),
  b("Escape", "back", "global", 2, "General", "Back / close"),

  // Setup wizard
  b("Enter", "wizard.next", "wizard", 2, "Setup", "Continue"),

  // Move
  b("j", "list.down", "main", 2, "Move", "Down"),
  b("k", "list.up", "main", 2, "Move", "Up"),
  b("g g", "list.first", "main", 2, "Move", "First"),
  b("G", "list.last", "main", 2, "Move", "Last (or line N)"),
  b("C-d", "list.halfDown", "main", 2, "Move", "Half page down"),
  b("C-u", "list.halfUp", "main", 2, "Move", "Half page up"),
  b("C-f", "list.pageDown", "main", 2, "Move", "Page down"),
  b("C-b", "list.pageUp", "main", 2, "Move", "Page up"),
  b("h", "pane.left", "main", 2, "Panes", "Focus pane left"),
  b("l", "pane.right", "main", 2, "Panes", "Focus pane right"),
  b("Tab", "pane.next", "main", 2, "Panes", "Next pane"),
  b("o", "open", "main", 2, "Open", "Open"),
  b("Enter", "open", "main", 2, "Open", "Open"),
  b("]", "message.next", "main", 2, "Open", "Next message"),
  b("[", "message.prev", "main", 2, "Open", "Previous message"),
  b("/", "search.filter", "main", 2, "Find", "Filter by subject"),

  // Go to
  b("g i", "goto.inbox", "main", 2, "Go to", "Inbox"),
  b("g s", "goto.sent", "main", 2, "Go to", "Sent"),
  b("g d", "goto.drafts", "main", 2, "Go to", "Drafts"),
  b("g t", "goto.trash", "main", 2, "Go to", "Trash"),
  b("g a", "goto.archive", "main", 2, "Go to", "Archive"),
  b("g j", "goto.spam", "main", 2, "Go to", "Spam"),
  b("g l", "goto.pick", "main", 2, "Go to", "Pick a folder or label"),
  b("g r", "sync.refresh", "main", 2, "Go to", "Refresh"),

  // Message view
  b("i", "view.images", "main", 2, "Message", "Load images for this message"),
  b("g h", "view.plain", "main", 2, "Message", "Plain text / HTML"),
  b("y", "copy.sender", "main", 2, "Message", "Copy sender address"),

  // Reserved: kept free so the map stays consistent. Nothing here acts yet.
  b("e", "msg.archive", "main", 4, "Later", "Archive"),
  b("#", "msg.trash", "main", 4, "Later", "Trash"),
  b("!", "msg.spam", "main", 4, "Later", "Mark as spam"),
  b("m", "msg.move", "main", 4, "Later", "Move to folder"),
  b(";", "msg.label", "main", 4, "Later", "Label"),
  b("s", "msg.flag", "main", 4, "Later", "Star"),
  b("U", "msg.unread", "main", 4, "Later", "Mark unread"),
  b("x", "msg.select", "main", 4, "Later", "Select"),
  b("u", "msg.undo", "main", 4, "Later", "Undo"),
  b("a", "llm.accept", "main", 3, "Later", "Accept suggestion"),
  b("S", "llm.panel", "main", 3, "Later", "Show suggestion"),
  b("r", "msg.reply", "main", 5, "Later", "Reply (draft)"),
  b("R", "msg.replyAll", "main", 5, "Later", "Reply all (draft)"),
];

export const isReserved = (x: Binding): boolean => x.phase > 2;

export const formatKeys = (keys: string[]): string =>
  keys.map((k) => (k.startsWith("C-") ? `Ctrl-${k.slice(2)}` : k)).join(" ");

/** Bindings that start with `prefix` and continue, in the given contexts. */
export function continuations(
  prefix: string[],
  contexts: readonly Context[],
): Binding[] {
  return keymap.filter(
    (x) =>
      contexts.includes(x.context) &&
      x.keys.length > prefix.length &&
      prefix.every((k, i) => x.keys[i] === k),
  );
}
