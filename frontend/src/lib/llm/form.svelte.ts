import * as api from "../api";
import { isKeyReentry, isListUnsupported, saveErrorCopy } from "./errors";
import { kindFor, presetById, type PresetId } from "./presets";
import { validate, type FieldErrors } from "./validate";

export type ModelsState =
  "idle" | "loading" | "ready" | "unsupported" | "error";

export interface FormError {
  title: string;
  body: string;
}

export class ProviderForm {
  mode = $state<"create" | "edit">("create");
  preset = $state<PresetId>("custom");
  name = $state("");
  baseURL = $state("");
  model = $state("");
  enabled = $state(true);
  structuredOutput = $state(false);
  tools = $state(false);
  maxContext = $state(0);
  /** Write-only. Never filled from the provider, cleared after every submit. */
  apiKey = $state("");
  hasKey = $state(false);
  keyRequired = $state(false);

  models = $state<string[]>([]);
  modelsState = $state<ModelsState>("idle");
  manualModel = $state(false);

  busy = $state(false);
  error = $state<FormError | null>(null);
  showErrors = $state(false);
  /** Set when the address changed and the old key must be entered again. */
  keyNeeded = $state(false);
  /** The user must confirm turning on a cloud provider. */
  confirmEnable = $state(false);

  private originalURL = "";
  private originalEnabled = false;

  constructor(
    private readonly existing: () => string[],
    private readonly onSaved: (name: string, test: boolean) => void,
  ) {}

  get kind(): "local" | "cloud" {
    return kindFor(this.baseURL);
  }
  get errors(): FieldErrors {
    return validate({
      name: this.name,
      baseURL: this.baseURL,
      model: this.model,
      apiKey: this.apiKey,
      maxContext: Number(this.maxContext),
      mode: this.mode,
      hasKey: this.hasKey,
      keyRequired:
        this.keyRequired || (this.kind === "cloud" && this.mode === "create"),
      existing: this.existing(),
    });
  }
  get urlChanged(): boolean {
    return this.mode === "edit" && this.baseURL.trim() !== this.originalURL;
  }

  startCreate(preset: PresetId): void {
    this.mode = "create";
    this.applyPreset(preset);
    this.model = "";
    this.apiKey = "";
    this.hasKey = false;
    this.resetState();
  }

  startEdit(p: api.LLMProvider): void {
    this.mode = "edit";
    this.preset = "custom";
    this.name = p.name;
    this.baseURL = p.baseURL;
    this.model = p.model;
    this.enabled = p.enabled;
    this.structuredOutput = p.capabilities.structuredOutput;
    this.tools = p.capabilities.tools;
    this.maxContext = p.capabilities.maxContext;
    this.apiKey = ""; // never prefilled
    this.hasKey = p.hasKey;
    this.keyRequired = false;
    this.originalURL = p.baseURL;
    this.originalEnabled = p.enabled;
    this.resetState();
  }

  applyPreset(id: PresetId): void {
    const p = presetById(id);
    this.preset = id;
    if (this.mode === "create") this.name = p.name;
    this.baseURL = p.baseURL;
    this.keyRequired = p.keyRequired;
    this.structuredOutput = p.capabilities.structuredOutput;
    this.tools = p.capabilities.tools;
    this.maxContext = p.capabilities.maxContext;
    // Cloud providers start switched off: nothing is sent until you turn them on.
    this.enabled = p.kind === "local";
    this.models = [];
    this.modelsState = "idle";
    this.manualModel = false;
  }

  private resetState(): void {
    this.error = null;
    this.showErrors = false;
    this.keyNeeded = false;
    this.confirmEnable = false;
    this.models = [];
    this.modelsState = "idle";
    this.manualModel = false;
    this.busy = false;
  }

  private input(enabled: boolean): api.LLMProviderIn {
    return {
      name: this.name.trim(),
      baseURL: this.baseURL.trim(),
      model: this.model.trim(),
      kind: this.kind,
      enabled,
      capabilities: {
        structuredOutput: this.structuredOutput,
        tools: this.tools,
        maxContext: Math.max(0, Math.trunc(Number(this.maxContext) || 0)),
      },
      apiKey: this.apiKey,
    };
  }

  /** Save. The key is cleared as soon as the call returns, success or not. */
  private async persist(enabled: boolean): Promise<boolean> {
    const typedKey = this.apiKey !== "";
    this.busy = true;
    this.error = null;
    this.keyNeeded = false;
    try {
      await api.saveProvider(this.input(enabled));
    } catch (e) {
      this.error = saveErrorCopy(e);
      this.keyNeeded = isKeyReentry(e);
      return false;
    } finally {
      this.apiKey = "";
      this.busy = false;
    }
    if (typedKey) this.hasKey = true;
    this.mode = "edit";
    this.originalURL = this.baseURL.trim();
    this.originalEnabled = enabled;
    return true;
  }

  /**
   * Validate, ask for confirmation when turning on a cloud provider, then save.
   * Returns "saved", "confirm" (waiting for the user) or "invalid"/"failed".
   */
  async submit(
    opts: { test?: boolean; confirmed?: boolean } = {},
  ): Promise<"saved" | "confirm" | "invalid" | "failed"> {
    if (this.busy) return "failed";
    this.showErrors = true;
    if (Object.keys(this.errors).length > 0) return "invalid";
    const turningOn =
      this.kind === "cloud" && this.enabled && !this.originalEnabled;
    if (turningOn && !opts.confirmed) {
      this.confirmEnable = true;
      return "confirm";
    }
    this.confirmEnable = false;
    if (!(await this.persist(this.enabled))) return "failed";
    this.onSaved(this.name.trim(), !!opts.test);
    return "saved";
  }

  /** Load the model list. A new or changed provider is saved first (switched off if cloud). */
  async loadModels(): Promise<void> {
    if (this.busy) return;
    this.showErrors = true;
    const e = this.errors;
    if (e.name || e.baseURL || e.apiKey) return;
    const needsSave =
      this.mode === "create" || this.urlChanged || this.apiKey !== "";
    if (needsSave) {
      const enabled =
        this.mode === "create" ? this.kind === "local" : this.originalEnabled;
      if (!(await this.persist(enabled))) return;
      this.onSaved(this.name.trim(), false);
    }
    this.modelsState = "loading";
    try {
      const ids = (await api.listModels(this.name.trim())) ?? [];
      this.models = ids;
      this.modelsState = ids.length > 0 ? "ready" : "unsupported";
      this.manualModel = ids.length === 0;
    } catch (err) {
      this.models = [];
      this.manualModel = true;
      this.modelsState = isListUnsupported(err) ? "unsupported" : "error";
    }
  }

  chooseModel(value: string): void {
    if (value === "__manual__") {
      this.manualModel = true;
      return;
    }
    this.model = value;
  }
}
