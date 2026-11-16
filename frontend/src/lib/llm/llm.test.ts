import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../api", () => ({
  listProviders: vi.fn(),
  saveProvider: vi.fn(),
  deleteProvider: vi.fn(),
  setDefaultProvider: vi.fn(),
  testProvider: vi.fn(),
  listModels: vi.fn(),
}));

import * as api from "../api";
import { ProviderForm } from "./form.svelte";
import { kindFor, presetById } from "./presets";
import { validate, type FormValues } from "./validate";
import { isKeyReentry, saveErrorCopy, testCopy } from "./errors";

const m = vi.mocked(api);

const base: FormValues = {
  name: "ollama",
  baseURL: "http://127.0.0.1:11434/v1",
  model: "",
  apiKey: "",
  maxContext: 0,
  mode: "create",
  hasKey: false,
  keyRequired: false,
  existing: [],
};

const provider = (o: Partial<api.LLMProvider> = {}): api.LLMProvider => ({
  name: "openrouter",
  baseURL: "https://openrouter.ai/api/v1",
  model: "x/y",
  kind: "cloud",
  enabled: false,
  capabilities: { structuredOutput: false, tools: false, maxContext: 0 },
  thinking: "",
  isDefault: false,
  hasKey: true,
  ...o,
});

let saved: string[];
let form: ProviderForm;
beforeEach(() => {
  vi.resetAllMocks();
  saved = [];
  m.saveProvider.mockResolvedValue(undefined);
  form = new ProviderForm(
    () => ["taken"],
    (n) => saved.push(n),
  );
});

describe("validate", () => {
  it("accepts a good local provider", () => {
    expect(validate(base)).toEqual({});
  });
  it.each(["", "-bad", "has space", "a".repeat(65), "naïve"])(
    "rejects the name %j",
    (name) => {
      expect(validate({ ...base, name }).name).toBeTruthy();
    },
  );
  it("accepts names up to 64 characters", () => {
    expect(validate({ ...base, name: "a".repeat(64) }).name).toBeUndefined();
    expect(validate({ ...base, name: "A_b-9" }).name).toBeUndefined();
  });
  it("won't let a new provider overwrite an existing one", () => {
    expect(
      validate({ ...base, name: "Taken", existing: ["taken"] }).name,
    ).toMatch(/already exists/);
    // Editing keeps the name, so it isn't checked.
    expect(
      validate({ ...base, mode: "edit", name: "taken", existing: ["taken"] })
        .name,
    ).toBeUndefined();
  });
  it("rejects names that would share an environment variable with another", () => {
    const existing = ["open-ai", "Work_1"];
    expect(validate({ ...base, name: "open_ai", existing }).name).toMatch(
      /Too close to “open-ai”/,
    );
    expect(validate({ ...base, name: "OPEN-AI", existing }).name).toBeTruthy();
    expect(validate({ ...base, name: "work-1", existing }).name).toBeTruthy();
    expect(
      validate({ ...base, name: "open-ai2", existing }).name,
    ).toBeUndefined();
  });
  it("wants a plain address: no credentials, query or fragment", () => {
    const u = (baseURL: string) => validate({ ...base, baseURL }).baseURL;
    expect(u("https://user:pw@example.com/v1")).toMatch(
      /username and password/,
    );
    expect(u("https://user@example.com/v1")).toMatch(/username and password/);
    expect(u("https://example.com/v1?token=1")).toMatch(/query/);
    expect(u("https://example.com/v1?")).toMatch(/query/);
    expect(u("https://example.com/v1#x")).toMatch(/#/);
    expect(u("https://example.com/v1")).toBeUndefined();
    expect(u("http://127.0.0.1:11434/v1")).toBeUndefined();
  });
  it("checks the address", () => {
    expect(validate({ ...base, baseURL: "" }).baseURL).toBeTruthy();
    expect(validate({ ...base, baseURL: "ftp://x" }).baseURL).toMatch(/http/);
    expect(validate({ ...base, baseURL: "not a url" }).baseURL).toBeTruthy();
    expect(
      validate({ ...base, baseURL: "http://example.com/v1" }).baseURL,
    ).toMatch(/https/);
    expect(
      validate({ ...base, baseURL: "https://example.com/v1" }).baseURL,
    ).toBeUndefined();
  });
  it("requires a key when the service needs one and none is stored", () => {
    const v = {
      ...base,
      baseURL: "https://openrouter.ai/api/v1",
      keyRequired: true,
    };
    expect(validate(v).apiKey).toBeTruthy();
    expect(validate({ ...v, hasKey: true }).apiKey).toBeUndefined();
    expect(validate({ ...v, apiKey: "sk-1" }).apiKey).toBeUndefined();
  });
  it("checks limits", () => {
    expect(validate({ ...base, model: "m".repeat(201) }).model).toBeTruthy();
    expect(validate({ ...base, apiKey: "k".repeat(4097) }).apiKey).toBeTruthy();
    expect(validate({ ...base, maxContext: -1 }).maxContext).toBeTruthy();
    expect(validate({ ...base, maxContext: 1.5 }).maxContext).toBeTruthy();
  });
});

describe("presets", () => {
  it("knows local and cloud addresses", () => {
    expect(kindFor("http://127.0.0.1:11434/v1")).toBe("local");
    expect(kindFor("http://localhost:1234")).toBe("local");
    expect(kindFor("http://[::1]:8080")).toBe("local");
    expect(kindFor("https://openrouter.ai/api/v1")).toBe("cloud");
    expect(kindFor("nonsense")).toBe("cloud");
  });
  it("Ollama is local, on, and needs no key", () => {
    form.startCreate("ollama");
    expect(form.name).toBe("ollama");
    expect(form.baseURL).toBe("http://127.0.0.1:11434/v1");
    expect(form.kind).toBe("local");
    expect(form.enabled).toBe(true);
    expect(form.keyRequired).toBe(false);
    expect(presetById("ollama").kind).toBe("local");
  });
  it("OpenRouter is cloud, off by default, and needs a key", () => {
    form.startCreate("openrouter");
    expect(form.baseURL).toBe("https://openrouter.ai/api/v1");
    expect(form.kind).toBe("cloud");
    expect(form.enabled).toBe(false);
    expect(form.keyRequired).toBe(true);
    expect(form.errors.apiKey).toBeTruthy();
  });
  it("Custom starts empty", () => {
    form.startCreate("custom");
    expect(form.name).toBe("");
    expect(form.baseURL).toBe("");
  });
});

describe("saving", () => {
  it("sends the key once and clears it afterwards", async () => {
    form.startCreate("openrouter");
    form.apiKey = "sk-secret";
    expect(await form.submit({ confirmed: true })).toBe("saved");
    expect(m.saveProvider).toHaveBeenCalledWith(
      expect.objectContaining({
        name: "openrouter",
        kind: "cloud",
        enabled: false,
        apiKey: "sk-secret",
      }),
    );
    expect(form.apiKey).toBe("");
    expect(form.hasKey).toBe(true);
    expect(JSON.stringify(form.error)).not.toContain("sk-secret");
    expect(saved).toEqual(["openrouter"]);
  });

  it("clears the key even when saving fails", async () => {
    m.saveProvider.mockRejectedValue(new Error("save config: disk full"));
    form.startCreate("openrouter");
    form.apiKey = "sk-secret";
    expect(await form.submit({ confirmed: true })).toBe("failed");
    expect(form.apiKey).toBe("");
    expect(form.error?.title).toMatch(/save/i);
    expect(JSON.stringify(form.error)).not.toContain("sk-secret");
  });

  it("never prefills the key when editing", () => {
    form.startEdit(provider());
    expect(form.apiKey).toBe("");
    expect(form.hasKey).toBe(true);
    expect(form.mode).toBe("edit");
  });

  it("an empty key on edit is sent empty, so the stored key is kept", async () => {
    form.startEdit(provider({ enabled: true }));
    form.model = "x/z";
    expect(await form.submit()).toBe("saved");
    expect(m.saveProvider).toHaveBeenCalledWith(
      expect.objectContaining({ apiKey: "", model: "x/z", name: "openrouter" }),
    );
  });

  it("turns the switch off and shows a calm notice when the OS dialog is declined", async () => {
    m.saveProvider.mockRejectedValue(
      new Error(
        "enabling a cloud LLM provider was not confirmed; nothing was saved",
      ),
    );
    form.startCreate("openrouter");
    form.apiKey = "sk-1";
    form.enabled = true;
    expect(await form.submit({ confirmed: true })).toBe("failed");
    expect(form.notice).toBe("The provider stays off. Nothing was saved.");
    expect(form.enabled).toBe(false);
    expect(form.error).toBeNull();
    expect(form.apiKey).toBe("");
    expect(form.mode).toBe("create");
  });

  it("treats a local provider pointed at the cloud while on as turning one on", async () => {
    form.startEdit(
      provider({
        name: "ollama",
        baseURL: "http://127.0.0.1:11434/v1",
        kind: "local",
        enabled: true,
      }),
    );
    form.baseURL = "https://api.example.com/v1";
    form.apiKey = "k";
    expect(await form.submit()).toBe("confirm");
    expect(m.saveProvider).not.toHaveBeenCalled();
  });

  it("listing models never switches a cloud provider on while saving", async () => {
    m.listModels.mockResolvedValue(["a"]);
    form.startEdit(
      provider({
        name: "ollama",
        baseURL: "http://127.0.0.1:11434/v1",
        kind: "local",
        enabled: true,
        hasKey: false,
      }),
    );
    form.baseURL = "https://api.example.com/v1";
    form.apiKey = "k";
    await form.loadModels();
    expect(m.saveProvider).toHaveBeenCalledWith(
      expect.objectContaining({ enabled: false }),
    );
  });

  it("explains the re-entry rule when the address changed", async () => {
    m.saveProvider.mockRejectedValue(
      new Error(
        "key_needs_reentry: an API key exists that was not approved for this base URL; enter the API key again",
      ),
    );
    form.startEdit(provider());
    form.baseURL = "https://other.example/v1";
    expect(form.urlChanged).toBe(true);
    expect(await form.submit()).toBe("failed");
    expect(form.keyNeeded).toBe(true);
    expect(form.error?.body).toMatch(/re-enter the API key/i);
    expect(form.apiKey).toBe("");
  });

  it("is invalid (and doesn't call Go) for a duplicate name or bad address", async () => {
    form.startCreate("custom");
    form.name = "taken";
    form.baseURL = "nope";
    expect(await form.submit()).toBe("invalid");
    expect(m.saveProvider).not.toHaveBeenCalled();
    expect(form.errors.name).toBeTruthy();
    expect(form.errors.baseURL).toBeTruthy();
  });

  it("asks before turning on a cloud provider", async () => {
    form.startEdit(provider({ enabled: false }));
    form.enabled = true;
    expect(await form.submit()).toBe("confirm");
    expect(form.confirmEnable).toBe(true);
    expect(m.saveProvider).not.toHaveBeenCalled();
    expect(await form.submit({ confirmed: true })).toBe("saved");
    expect(m.saveProvider).toHaveBeenCalledWith(
      expect.objectContaining({ enabled: true }),
    );
  });

  it("doesn't ask for local providers or for ones already on", async () => {
    form.startCreate("ollama");
    expect(await form.submit()).toBe("saved");
    form.startEdit(provider({ enabled: true }));
    expect(await form.submit()).toBe("saved");
  });

  it("asks the caller to run a test after Save and test", async () => {
    form.startCreate("ollama");
    const calls: boolean[] = [];
    const f = new ProviderForm(
      () => [],
      (_n, t) => calls.push(t),
    );
    f.startCreate("ollama");
    await f.submit({ test: true });
    expect(calls).toEqual([true]);
  });
});

describe("models", () => {
  it("saves a new provider first (cloud stays off), then lists models", async () => {
    m.listModels.mockResolvedValue(["a", "b"]);
    form.startCreate("openrouter");
    form.apiKey = "sk-1";
    await form.loadModels();
    expect(m.saveProvider).toHaveBeenCalledWith(
      expect.objectContaining({ enabled: false, apiKey: "sk-1" }),
    );
    expect(m.listModels).toHaveBeenCalledWith("openrouter");
    expect(form.models).toEqual(["a", "b"]);
    expect(form.modelsState).toBe("ready");
    expect(form.apiKey).toBe("");
    expect(form.mode).toBe("edit");
  });

  it("falls back to typing the model name when the endpoint can't list", async () => {
    m.listModels.mockRejectedValue(
      new Error("this endpoint does not list models"),
    );
    form.startCreate("ollama");
    await form.loadModels();
    expect(form.modelsState).toBe("unsupported");
    expect(form.manualModel).toBe(true);
    expect(form.models).toEqual([]);
  });

  it("also falls back on an empty list and on other errors", async () => {
    m.listModels.mockResolvedValueOnce([]);
    form.startCreate("ollama");
    await form.loadModels();
    expect(form.modelsState).toBe("unsupported");
    m.listModels.mockRejectedValueOnce(new Error("boom"));
    await form.loadModels();
    expect(form.modelsState).toBe("error");
    expect(form.manualModel).toBe(true);
  });

  it("doesn't save again when nothing changed", async () => {
    m.listModels.mockResolvedValue(["a"]);
    form.startEdit(provider({ enabled: true }));
    await form.loadModels();
    expect(m.saveProvider).not.toHaveBeenCalled();
  });

  it("lets you pick a listed model or choose to type one", () => {
    form.chooseModel("a");
    expect(form.model).toBe("a");
    form.chooseModel("__manual__");
    expect(form.manualModel).toBe(true);
    expect(form.model).toBe("a");
  });
});

describe("error copy", () => {
  const cats = [
    "ok",
    "unreachable",
    "unauthorized",
    "model_not_found",
    "structured_output_unsupported",
    "timeout",
    "cloud_disabled",
    "no_model",
    "keyring_unavailable",
    "other",
    "key_needs_reentry",
  ];
  it.each(cats)("%s has a title and a hint", (c) => {
    const t = testCopy(c);
    expect(t.title).not.toBe("");
    expect(t.hint).not.toBe("");
  });
  it("unknown categories use the generic copy", () => {
    expect(testCopy("brand_new")).toEqual(testCopy("other"));
  });
  it("matches re-entry only on the key_needs_reentry: prefix", () => {
    expect(isKeyReentry(new Error("key_needs_reentry: anything"))).toBe(true);
    expect(isKeyReentry(new Error("save: key_needs_reentry: x"))).toBe(true);
    expect(isKeyReentry(new Error("please enter the API key again"))).toBe(
      false,
    );
    expect(isKeyReentry(new Error("the base URL changed"))).toBe(false);
  });
  it("re-entry offers the key action", () => {
    expect(testCopy("key_needs_reentry").action).toBe("key");
  });
  it("failures offer a recovery action, success does not", () => {
    expect(testCopy("ok").action).toBeUndefined();
    expect(testCopy("unauthorized").action).toBe("edit");
    expect(testCopy("cloud_disabled").action).toBe("enable");
    expect(testCopy("unreachable").action).toBe("retry");
  });
  it("maps the new Go validation messages to friendly words", () => {
    expect(
      saveErrorCopy(
        new Error(
          'invalid input: invalid config: llm provider "a": name "a" collides with "A" (both read X)',
        ),
      ).title,
    ).toMatch(/too close/i);
    expect(
      saveErrorCopy(
        new Error("invalid input: baseURL: must not contain a query"),
      ).title,
    ).toMatch(/plain/i);
    expect(
      saveErrorCopy(
        new Error(
          "the provider changed while it was being confirmed; try again",
        ),
      ).title,
    ).toMatch(/changed/i);
  });
  it("re-entry copy depends on where it happens", () => {
    const e = new Error(
      "key_needs_reentry: an API key exists that was not approved for this base URL; enter the API key again",
    );
    expect(
      saveErrorCopy(e, { mode: "create", urlChanged: false }).body,
    ).toMatch(/already stored under this name/);
    expect(saveErrorCopy(e, { mode: "edit", urlChanged: true }).body).toMatch(
      /changed the address/,
    );
    expect(saveErrorCopy(e, { mode: "edit", urlChanged: false }).body).toMatch(
      /re-enter the key once/,
    );
  });
  it("save errors never echo unknown text", () => {
    const c = saveErrorCopy(new Error("secret sk-123 exploded"));
    expect(JSON.stringify(c)).not.toContain("sk-123");
  });
});
