import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/svelte";

vi.mock("../api", () => ({
  listMailboxes: vi.fn(async () => []),
  syncStatus: vi.fn(async () => ({
    running: false,
    stopTimedOut: false,
    lastResult: null,
    lastError: "",
  })),
  onEvent: vi.fn(() => () => {}),
}));

import SyncList from "./SyncList.svelte";
import WhichKey from "./WhichKey.svelte";
import { sync } from "../stores/sync.svelte";
import { ui } from "../stores/ui.svelte";

beforeEach(() => {
  sync.reset();
  ui.setPending([], undefined);
  ui.whichKey = false;
  ui.context = "main";
});

describe("SyncList", () => {
  it("lists folders by state with counts, bars and failure reasons", () => {
    sync.onProgress({
      state: "headers",
      mailbox: "INBOX",
      headersDone: 5,
      headersTotal: 10,
      bodiesDone: 0,
      bodiesTotal: 0,
    });
    sync.onDone({
      mailboxes: 2,
      headersFetched: 0,
      deleted: 0,
      bodiesFetched: 0,
      bodiesFailed: 0,
      mailboxErrors: [{ mailbox: "Sent", message: "timed out" }],
    });
    sync.flush();
    render(SyncList);
    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent("INBOX");
    expect(items[0]).toHaveTextContent("Up to date");
    expect(items[1]).toHaveTextContent("Couldn't sync");
    expect(items[1]).toHaveTextContent("timed out");
    expect(screen.getAllByRole("progressbar")).toHaveLength(2);
  });
});

describe("WhichKey", () => {
  it("shows clickable entries that stay up and runs the picked one", async () => {
    const trigger = vi.fn();
    ui.triggerBinding = trigger;
    ui.setPending(["g"], undefined);
    ui.whichKey = true;
    render(WhichKey);
    const inbox = screen.getByRole("button", { name: /Inbox/ });
    await fireEvent.click(inbox);
    expect(trigger).toHaveBeenCalledWith(
      expect.objectContaining({ action: "goto.inbox" }),
    );
  });

  it("announces the options politely", () => {
    ui.setPending(["g"], undefined);
    render(WhichKey);
    const live = screen.getByRole("status");
    expect(live).toHaveAttribute("aria-live", "polite");
    expect(live).toHaveTextContent(/g pressed\. Options:.*i Inbox/);
  });
});
