import { kindFor } from "./presets";

export const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/;
export const MAX_MODEL = 200;
export const MAX_KEY = 4096;
export const MAX_CONTEXT = 10_000_000;

export interface FormValues {
  name: string;
  baseURL: string;
  model: string;
  apiKey: string;
  maxContext: number;
  mode: "create" | "edit";
  hasKey: boolean;
  keyRequired: boolean;
  /** Names that already exist (so a "new" provider can't overwrite one). */
  existing: string[];
}

export type FieldErrors = Partial<
  Record<"name" | "baseURL" | "model" | "apiKey" | "maxContext", string>
>;

/** Plain-language field errors. Empty object means valid. */
export function validate(v: FormValues): FieldErrors {
  const e: FieldErrors = {};
  const name = v.name.trim();
  if (v.mode === "create") {
    if (!name) e.name = "Give it a name.";
    else if (!NAME_RE.test(name))
      e.name =
        "Use letters, digits, - or _ (up to 64), starting with a letter or digit.";
    else if (v.existing.some((n) => n.toLowerCase() === name.toLowerCase()))
      e.name = "A provider with this name already exists.";
  }
  const url = v.baseURL.trim();
  if (!url) e.baseURL = "Enter the address of the service.";
  else {
    try {
      const u = new URL(url);
      if (u.protocol !== "http:" && u.protocol !== "https:")
        e.baseURL = "The address must start with http:// or https://.";
      else if (u.protocol === "http:" && kindFor(url) === "cloud")
        e.baseURL = "Use https:// for services outside this computer.";
    } catch {
      e.baseURL =
        "That doesn't look like an address, for example https://host/v1.";
    }
  }
  if (v.model.length > MAX_MODEL) e.model = "That model name is too long.";
  if (v.apiKey.length > MAX_KEY) e.apiKey = "That key is too long.";
  else if (v.keyRequired && !v.hasKey && !v.apiKey)
    e.apiKey = "This service needs an API key.";
  if (
    !Number.isInteger(v.maxContext) ||
    v.maxContext < 0 ||
    v.maxContext > MAX_CONTEXT
  )
    e.maxContext = "Use a whole number, or 0 if you don't know.";
  return e;
}
