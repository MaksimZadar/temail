import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/svelte";

vi.mock("./lib/api", () => ({
  getStatus: vi.fn(),
  saveAccount: vi.fn(),
  probeCert: vi.fn(),
  trustCert: vi.fn(),
  testConnection: vi.fn(),
  syncStart: vi.fn(async () => {}),
  listMailboxes: vi.fn(async () => []),
  listMessages: vi.fn(async () => ({ messages: [], next: null })),
  syncStatus: vi.fn(async () => ({
    running: false,
    stopTimedOut: false,
    lastResult: null,
    lastError: "",
  })),
  onEvent: vi.fn(() => () => {}),
}));

import * as api from "./lib/api";
import App from "./App.svelte";
import { app } from "./lib/stores/app.svelte";
import { ui } from "./lib/stores/ui.svelte";

const m = vi.mocked(api);
const st = (o: Partial<api.SetupStatus> = {}): api.SetupStatus => ({
  configured: false,
  keyringOk: true,
  keyringMessage: "",
  bridgeReachable: true,
  certPinned: false,
  accountId: "",
  ...o,
});

beforeEach(() => {
  vi.clearAllMocks();
  app.route = "loading";
  ui.overlay = "none";
});

describe("App", () => {
  it("shows the wizard when setup is incomplete", async () => {
    m.getStatus.mockResolvedValue(st());
    render(App);
    expect(
      await screen.findByRole("heading", { name: "Welcome to temail" }),
    ).toBeInTheDocument();
    expect(
      await screen.findByText("Proton Bridge is running"),
    ).toBeInTheDocument();
  });

  it("shows the mailbox view when configured and pinned", async () => {
    m.getStatus.mockResolvedValue(st({ configured: true, certPinned: true }));
    render(App);
    expect(
      await screen.findByRole("toolbar", { name: "Mailbox" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("navigation", { name: "Folders" }),
    ).toBeInTheDocument();
  });

  it("explains a missing keyring and keeps Continue disabled", async () => {
    m.getStatus.mockResolvedValue(
      st({ keyringOk: false, keyringMessage: "locked" }),
    );
    render(App);
    expect(
      await screen.findByText(/Can't reach the system keyring/),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();
  });

  it("walks the wizard with the keyboard and mouse, clearing the password", async () => {
    m.getStatus.mockResolvedValue(st());
    m.saveAccount.mockResolvedValue(undefined);
    m.probeCert.mockResolvedValue({
      fingerprint: "AA:BB",
      subject: "127.0.0.1",
    });
    render(App);
    await screen.findByText("Proton Bridge is running");
    await fireEvent.keyDown(window, { key: "Enter" }); // Enter continues
    await screen.findByRole("heading", { name: "Connect to Bridge" });
    const pw = screen.getByLabelText("Bridge password") as HTMLInputElement;
    await fireEvent.input(screen.getByLabelText("Bridge username"), {
      target: { value: "me@proton.me" },
    });
    await fireEvent.input(pw, { target: { value: "secret-pw" } });
    await fireEvent.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByRole("heading", { name: "Check this connection" });
    expect(m.saveAccount).toHaveBeenCalledOnce();
    expect(screen.queryByDisplayValue("secret-pw")).toBeNull();
    expect(await screen.findByText("AA:BB")).toBeInTheDocument();
  });

  it("shows an error in an alert region for a wrong password", async () => {
    m.getStatus.mockResolvedValue(st({ configured: true, certPinned: false }));
    m.probeCert.mockResolvedValue({ fingerprint: "AA", subject: "" });
    m.trustCert.mockResolvedValue(undefined);
    m.testConnection.mockResolvedValue({
      ok: false,
      category: "auth_failed",
      message: "",
    });
    render(App);
    await fireEvent.click(
      await screen.findByRole("button", { name: "Trust this certificate" }),
    );
    const alert = await screen.findByText("Bridge didn't accept that password");
    expect(alert.closest("[role=alert]")).not.toBeNull();
  });

  it("opens the help overlay with ? and closes it with Escape", async () => {
    m.getStatus.mockResolvedValue(st());
    render(App);
    await screen.findByRole("heading", { name: "Welcome to temail" });
    await fireEvent.keyDown(window, { key: "?" });
    expect(
      await screen.findByRole("dialog", { name: "Keyboard shortcuts" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Coming later")).toBeInTheDocument();
    await fireEvent.keyDown(window, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("opens the palette with :", async () => {
    m.getStatus.mockResolvedValue(st());
    render(App);
    await screen.findByRole("heading", { name: "Welcome to temail" });
    await fireEvent.keyDown(window, { key: ":" });
    expect(
      await screen.findByRole("combobox", { name: "Command" }),
    ).toBeInTheDocument();
  });
});
