import type { LLMCapabilities } from "../api";

export type PresetId = "ollama" | "openrouter" | "custom";

export interface Preset {
  id: PresetId;
  label: string;
  blurb: string;
  name: string;
  baseURL: string;
  kind: "local" | "cloud";
  keyRequired: boolean;
  capabilities: LLMCapabilities;
}

export const PRESETS: Preset[] = [
  {
    id: "ollama",
    label: "Ollama",
    blurb: "Runs on this computer. Nothing leaves it. No key needed.",
    name: "ollama",
    baseURL: "http://127.0.0.1:11434/v1",
    kind: "local",
    keyRequired: false,
    capabilities: { structuredOutput: false, tools: false, maxContext: 0 },
  },
  {
    id: "openrouter",
    label: "OpenRouter",
    blurb: "A cloud service. Needs an API key. Email content is sent to it.",
    name: "openrouter",
    baseURL: "https://openrouter.ai/api/v1",
    kind: "cloud",
    keyRequired: true,
    capabilities: { structuredOutput: false, tools: false, maxContext: 0 },
  },
  {
    id: "custom",
    label: "Other (OpenAI-compatible)",
    blurb: "Any service that speaks the OpenAI API.",
    name: "",
    baseURL: "",
    kind: "cloud",
    keyRequired: false,
    capabilities: { structuredOutput: false, tools: false, maxContext: 0 },
  },
];

export const presetById = (id: PresetId): Preset =>
  PRESETS.find((p) => p.id === id) ?? PRESETS[2];

/** Same rule as the Go side: loopback is local, anything else is cloud. */
export function kindFor(baseURL: string): "local" | "cloud" {
  try {
    const h = new URL(baseURL).hostname.replace(/^\[|\]$/g, "");
    return h === "localhost" || h === "127.0.0.1" || h === "::1"
      ? "local"
      : "cloud";
  } catch {
    return "cloud";
  }
}
