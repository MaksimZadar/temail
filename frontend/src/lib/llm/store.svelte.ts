import * as api from "../api";
import { DECLINED_NOTICE, isEnableDeclined, isKeyReentry } from "./errors";

export type TestState =
  { busy: true } | { busy: false; result: api.ProviderTest };

class LLM {
  providers = $state<api.LLMProvider[]>([]);
  loaded = $state(false);
  loadError = $state("");
  tests = $state<Record<string, TestState>>({});
  /** Last action error (default, delete, enable), shown above the list. */
  actionError = $state("");
  /** A calm note, not an error (for example the OS confirmation was declined). */
  notice = $state("");

  async load(): Promise<void> {
    try {
      this.providers = (await api.listProviders()) ?? [];
      this.loadError = "";
    } catch {
      this.loadError = "Couldn't read the AI provider settings.";
    } finally {
      this.loaded = true;
    }
  }

  async test(name: string): Promise<api.ProviderTest> {
    this.tests = { ...this.tests, [name]: { busy: true } };
    let result: api.ProviderTest;
    try {
      result = await api.testProvider(name);
    } catch {
      result = {
        ok: false,
        category: "other",
        message: "The test could not run.",
      };
    }
    this.tests = { ...this.tests, [name]: { busy: false, result } };
    return result;
  }

  async setDefault(name: string): Promise<void> {
    await this.act(() => api.setDefaultProvider(name));
  }

  async remove(name: string): Promise<void> {
    await this.act(() => api.deleteProvider(name));
    const { [name]: _gone, ...rest } = this.tests;
    void _gone;
    this.tests = rest;
  }

  /** Turn a provider on or off, keeping everything else (and its key) as is. */
  async setEnabled(p: api.LLMProvider, enabled: boolean): Promise<void> {
    await this.act(() =>
      api.saveProvider({
        name: p.name,
        baseURL: p.baseURL,
        model: p.model,
        kind: p.kind,
        enabled,
        capabilities: p.capabilities,
        apiKey: "", // empty keeps the stored key
      }),
    );
  }

  private async act(fn: () => Promise<void>): Promise<void> {
    this.actionError = "";
    this.notice = "";
    try {
      await fn();
    } catch (e) {
      if (isEnableDeclined(e)) this.notice = DECLINED_NOTICE;
      else if (isKeyReentry(e))
        this.actionError = "Edit the provider and re-enter the API key.";
      else this.actionError = "That didn't work. Nothing was changed.";
    }
    await this.load();
  }
}

export const llm = new LLM();
