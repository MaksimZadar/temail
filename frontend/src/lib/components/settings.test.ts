import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  render,
  screen,
  fireEvent,
  waitFor,
  within,
} from "@testing-library/svelte";

vi.mock("../api", () => ({
  listMailboxes: vi.fn(async () => []),
  listMessages: vi.fn(async () => ({ messages: [], next: null })),
  getMessage: vi.fn(),
  cacheInfo: vi.fn(async () => ({ bytes: 0, messages: 0, bodies: 0 })),
  syncStart: vi.fn(async () => {}),
  syncStatus: vi.fn(async () => ({
    running: false,
    stopTimedOut: false,
    lastResult: null,
    lastError: "",
  })),
  getStatus: vi.fn(async () => ({
    configured: true,
    keyringOk: true,
    keyringMessage: "",
    bridgeReachable: true,
    certPinned: true,
    accountId: "main",
  })),
  onEvent: vi.fn(() => () => {}),
  listProviders: vi.fn(),
  saveProvider: vi.fn(async () => {}),
  deleteProvider: vi.fn(async () => {}),
  setDefaultProvider: vi.fn(async () => {}),
  testProvider: vi.fn(),
  listModels: vi.fn(),
}));

import * as api from "../api";
import App from "../../App.svelte";
import { app } from "../stores/app.svelte";
import { ui } from "../stores/ui.svelte";
import { llm } from "../llm/store.svelte";

const m = vi.mocked(api);

const prov = (o: Partial<api.LLMProvider> = {}): api.LLMProvider => ({
  name: "ollama",
  baseURL: "http://127.0.0.1:11434/v1",
  model: "llama3.1",
  kind: "local",
  enabled: true,
  capabilities: { structuredOutput: false, tools: false, maxContext: 0 },
  isDefault: false,
  hasKey: false,
  ...o,
});
const cloud = (o: Partial<api.LLMProvider> = {}) =>
  prov({
    name: "openrouter",
    baseURL: "https://openrouter.ai/api/v1",
    model: "x/y",
    kind: "cloud",
    enabled: false,
    hasKey: true,
    ...o,
  });

const key = (k: string, target: Element | Window = window) =>
  fireEvent.keyDown(target, { key: k });

async function openSettings(providers: api.LLMProvider[]) {
  m.listProviders.mockResolvedValue(providers);
  render(App);
  await screen.findByRole("toolbar", { name: "Mailbox" });
  await fireEvent.click(screen.getByRole("button", { name: "Settings" }));
  await screen.findByRole("heading", { name: "Settings" });
}

beforeEach(() => {
  vi.clearAllMocks();
  app.route = "main";
  ui.screen = "mail";
  ui.overlay = "none";
  llm.providers = [];
  llm.loaded = false;
  llm.tests = {};
  llm.actionError = "";
});

describe("opening and closing Settings", () => {
  it("opens from the toolbar and closes with Esc", async () => {
    await openSettings([prov()]);
    expect(ui.screen).toBe("settings");
    await key("Escape");
    expect(
      await screen.findByRole("toolbar", { name: "Mailbox" }),
    ).toBeInTheDocument();
    expect(ui.screen).toBe("mail");
  });

  it("opens with g then , from the keyboard", async () => {
    m.listProviders.mockResolvedValue([]);
    render(App);
    await screen.findByRole("toolbar", { name: "Mailbox" });
    await key("g");
    await key(",");
    expect(
      await screen.findByRole("heading", { name: "Settings" }),
    ).toBeInTheDocument();
    expect(ui.context).toBe("settings");
  });

  it("opens from the command palette", async () => {
    m.listProviders.mockResolvedValue([]);
    render(App);
    await screen.findByRole("toolbar", { name: "Mailbox" });
    await key(":");
    const input = await screen.findByRole("combobox", { name: "Command" });
    await fireEvent.input(input, { target: { value: "settings" } });
    await fireEvent.keyDown(input, { key: "Enter" });
    expect(
      await screen.findByRole("heading", { name: "Settings" }),
    ).toBeInTheDocument();
  });

  it("the Back button returns to mail", async () => {
    await openSettings([]);
    await fireEvent.click(screen.getByRole("button", { name: /Mail/ }));
    expect(
      await screen.findByRole("toolbar", { name: "Mailbox" }),
    ).toBeInTheDocument();
  });

  it("Esc closes the form first, then Settings", async () => {
    await openSettings([prov()]);
    await fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    await screen.findByRole("heading", { name: "Edit ollama" });
    await key("Escape");
    await waitFor(() =>
      expect(screen.queryByRole("heading", { name: "Edit ollama" })).toBeNull(),
    );
    expect(ui.screen).toBe("settings");
    await key("Escape");
    expect(ui.screen).toBe("mail");
  });
});

describe("provider list", () => {
  it("guides to Ollama or OpenRouter when empty", async () => {
    await openSettings([]);
    expect(await screen.findByText(/No AI provider yet/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Ollama/ })).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /OpenRouter/ }),
    ).toBeInTheDocument();
  });

  it("shows cards with kind, default, key and on/off state", async () => {
    await openSettings([prov({ isDefault: true }), cloud()]);
    const items = await screen.findAllByRole("listitem");
    const local = items.find((i) => i.getAttribute("aria-label") === "ollama")!;
    const remote = items.find(
      (i) => i.getAttribute("aria-label") === "openrouter",
    )!;
    expect(local).toHaveTextContent("Local");
    expect(local).toHaveTextContent("Default");
    expect(local).toHaveTextContent("Not needed");
    expect(within(local).queryByRole("switch")).toBeNull();
    expect(remote).toHaveTextContent("Cloud");
    expect(remote).toHaveTextContent("Saved");
    expect(within(remote).getByRole("switch")).not.toBeChecked();
    expect(remote).toHaveTextContent("Nothing is sent");
  });

  it("sets the default", async () => {
    await openSettings([prov(), cloud()]);
    const remote = (await screen.findAllByRole("listitem")).find(
      (i) => i.getAttribute("aria-label") === "openrouter",
    )!;
    await fireEvent.click(
      within(remote).getByRole("button", { name: "Set default" }),
    );
    expect(m.setDefaultProvider).toHaveBeenCalledWith("openrouter");
  });
});

describe("confirmations", () => {
  it("asks before deleting, and Cancel changes nothing", async () => {
    await openSettings([prov()]);
    await fireEvent.click(
      await screen.findByRole("button", { name: "Delete" }),
    );
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent(/Delete ollama\?/);
    expect(m.deleteProvider).not.toHaveBeenCalled();
    await fireEvent.click(
      within(dialog).getByRole("button", { name: "Cancel" }),
    );
    expect(m.deleteProvider).not.toHaveBeenCalled();
    await fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    await fireEvent.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", {
        name: "Delete",
      }),
    );
    await waitFor(() =>
      expect(m.deleteProvider).toHaveBeenCalledWith("ollama"),
    );
  });

  it("Esc cancels the delete dialog", async () => {
    await openSettings([prov()]);
    await fireEvent.click(
      await screen.findByRole("button", { name: "Delete" }),
    );
    await screen.findByRole("alertdialog");
    await key("Escape");
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(ui.screen).toBe("settings");
    expect(m.deleteProvider).not.toHaveBeenCalled();
  });

  it("asks before turning on a cloud provider, and keeps the stored key", async () => {
    await openSettings([cloud()]);
    await fireEvent.click(
      await screen.findByRole("switch", { name: "Use openrouter" }),
    );
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent(/receive email content/i);
    expect(m.saveProvider).not.toHaveBeenCalled();
    await fireEvent.click(
      within(dialog).getByRole("button", { name: "Turn on" }),
    );
    await waitFor(() =>
      expect(m.saveProvider).toHaveBeenCalledWith(
        expect.objectContaining({
          name: "openrouter",
          enabled: true,
          apiKey: "",
        }),
      ),
    );
  });

  it("turning a cloud provider off needs no confirmation", async () => {
    await openSettings([cloud({ enabled: true })]);
    await fireEvent.click(
      await screen.findByRole("switch", { name: "Use openrouter" }),
    );
    await waitFor(() =>
      expect(m.saveProvider).toHaveBeenCalledWith(
        expect.objectContaining({ enabled: false }),
      ),
    );
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });
});

describe("testing a provider", () => {
  it("shows friendly copy and a recovery action in a polite live region", async () => {
    m.testProvider.mockResolvedValue({
      ok: false,
      category: "unauthorized",
      message: "",
    });
    await openSettings([cloud({ enabled: true })]);
    await fireEvent.click(await screen.findByRole("button", { name: "Test" }));
    const msg = await screen.findByText("The API key was not accepted");
    expect(msg.closest("[aria-live=polite]")).not.toBeNull();
    await fireEvent.click(
      screen.getByRole("button", { name: "Edit provider" }),
    );
    expect(
      await screen.findByRole("heading", { name: "Edit openrouter" }),
    ).toBeInTheDocument();
  });

  it("shows success", async () => {
    m.testProvider.mockResolvedValue({ ok: true, category: "ok", message: "" });
    await openSettings([prov()]);
    await fireEvent.click(await screen.findByRole("button", { name: "Test" }));
    expect(await screen.findByText("It works")).toBeInTheDocument();
  });
});

describe("the form", () => {
  it("the API key is a password field and is never prefilled on edit", async () => {
    await openSettings([cloud()]);
    await fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    const keyInput = (await screen.findByLabelText(
      "API key",
    )) as HTMLInputElement;
    expect(keyInput.type).toBe("password");
    expect(keyInput.value).toBe("");
    expect(
      screen.getByText(/Leave blank to keep the saved key/),
    ).toBeInTheDocument();
    expect((screen.getByLabelText("Name") as HTMLInputElement).disabled).toBe(
      true,
    );
  });

  it("adds Ollama from a preset and clears nothing it shouldn't", async () => {
    await openSettings([]);
    await fireEvent.click(
      await screen.findByRole("button", { name: /Ollama/ }),
    );
    expect(
      ((await screen.findByLabelText("Name")) as HTMLInputElement).value,
    ).toBe("ollama");
    expect((screen.getByLabelText(/Address/) as HTMLInputElement).value).toBe(
      "http://127.0.0.1:11434/v1",
    );
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(m.saveProvider).toHaveBeenCalledWith(
        expect.objectContaining({
          name: "ollama",
          kind: "local",
          enabled: true,
          apiKey: "",
        }),
      ),
    );
  });

  it("shows validation errors and doesn't save", async () => {
    await openSettings([]);
    await fireEvent.click(await screen.findByRole("button", { name: /Other/ }));
    await screen.findByLabelText("Name");
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Give it a name.")).toBeInTheDocument();
    expect(
      screen.getByText("Enter the address of the service."),
    ).toBeInTheDocument();
    expect(m.saveProvider).not.toHaveBeenCalled();
  });

  it("OpenRouter shows the privacy note, needs a key, and confirms before turning on", async () => {
    await openSettings([]);
    await fireEvent.click(
      await screen.findByRole("button", { name: /OpenRouter/ }),
    );
    expect(
      await screen.findByText(/Nothing is sent until you turn it on/),
    ).toBeInTheDocument();
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(
      await screen.findByText("This service needs an API key."),
    ).toBeInTheDocument();
    const keyInput = screen.getByLabelText("API key") as HTMLInputElement;
    await fireEvent.input(keyInput, { target: { value: "sk-abc" } });
    await fireEvent.click(
      screen.getByRole("checkbox", { name: /Turn this provider on/ }),
    );
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(m.saveProvider).not.toHaveBeenCalled();
    await fireEvent.click(
      within(dialog).getByRole("button", { name: /Save and turn on/ }),
    );
    await waitFor(() =>
      expect(m.saveProvider).toHaveBeenCalledWith(
        expect.objectContaining({
          name: "openrouter",
          enabled: true,
          apiKey: "sk-abc",
        }),
      ),
    );
    // The key is gone from the UI after the call.
    expect(screen.queryByDisplayValue("sk-abc")).toBeNull();
  });

  it("explains a re-entry error and puts focus on the key field", async () => {
    m.saveProvider.mockRejectedValueOnce(
      new Error(
        "the base URL changed; enter the API key again so it is not sent to the new address",
      ),
    );
    await openSettings([cloud({ enabled: true })]);
    await fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    await fireEvent.input(await screen.findByLabelText(/Address/), {
      target: { value: "https://other.example/v1" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));
    const banner = await screen.findByText("Enter the API key again");
    expect(banner.closest("[role=alert]")).not.toBeNull();
    expect(screen.getAllByText(/re-enter the API key/i).length).toBeGreaterThan(
      0,
    );
    await fireEvent.click(
      screen.getByRole("button", { name: "Enter the key" }),
    );
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByLabelText("API key")),
    );
  });

  it("loads models into a dropdown, with a manual fallback", async () => {
    m.listModels.mockResolvedValue(["llama3.1", "qwen2.5"]);
    await openSettings([prov({ model: "" })]);
    await fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    await fireEvent.click(
      await screen.findByRole("button", { name: "Load models" }),
    );
    const select = (await screen.findByLabelText("Model")) as HTMLSelectElement;
    expect(
      within(select)
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(
      expect.arrayContaining(["llama3.1", "qwen2.5", "Type a name instead…"]),
    );
    await fireEvent.change(select, { target: { value: "qwen2.5" } });
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(m.saveProvider).toHaveBeenCalledWith(
        expect.objectContaining({ model: "qwen2.5" }),
      ),
    );
  });

  it("falls back to typing when the service can't list models", async () => {
    m.listModels.mockRejectedValue(
      new Error("this endpoint does not list models"),
    );
    await openSettings([prov({ model: "" })]);
    await fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    await fireEvent.click(
      await screen.findByRole("button", { name: "Load models" }),
    );
    expect(
      await screen.findByText(/doesn't list its models/),
    ).toBeInTheDocument();
    expect((screen.getByLabelText("Model") as HTMLInputElement).tagName).toBe(
      "INPUT",
    );
  });

  it("Save and test runs the test after saving", async () => {
    m.testProvider.mockResolvedValue({ ok: true, category: "ok", message: "" });
    await openSettings([]);
    await fireEvent.click(
      await screen.findByRole("button", { name: /Ollama/ }),
    );
    await fireEvent.click(
      await screen.findByRole("button", { name: "Save and test" }),
    );
    await waitFor(() => expect(m.testProvider).toHaveBeenCalledWith("ollama"));
  });
});
