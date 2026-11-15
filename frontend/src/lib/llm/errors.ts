export interface TestCopy {
  title: string;
  hint: string;
  /** Where the recovery button goes. */
  action?: "edit" | "enable" | "retry";
  actionLabel?: string;
}

// Plain-language copy for every TestProvider category.
const COPY: Record<string, TestCopy> = {
  ok: {
    title: "It works",
    hint: "The provider answered in the format temail needs.",
  },
  unreachable: {
    title: "Can't reach the provider",
    hint: "Check the address and that the service is running. For Ollama, start it and try again.",
    action: "retry",
    actionLabel: "Test again",
  },
  unauthorized: {
    title: "The API key was not accepted",
    hint: "Edit the provider and enter the key again.",
    action: "edit",
    actionLabel: "Edit provider",
  },
  model_not_found: {
    title: "Model or address not found",
    hint: "Check the model name (Load models can list them) and that the address ends in /v1.",
    action: "edit",
    actionLabel: "Edit provider",
  },
  structured_output_unsupported: {
    title: "This model didn't answer in the expected format",
    hint: "Turn off Structured output for this provider. temail will ask for JSON in the prompt instead.",
    action: "edit",
    actionLabel: "Edit provider",
  },
  timeout: {
    title: "The provider took too long",
    hint: "Local models can be slow the first time. Try again in a moment.",
    action: "retry",
    actionLabel: "Test again",
  },
  cloud_disabled: {
    title: "This cloud provider is turned off",
    hint: "Nothing is sent while it is off. Turn it on to test it.",
    action: "enable",
    actionLabel: "Turn on…",
  },
  no_model: {
    title: "Choose a model first",
    hint: "Edit the provider and pick or type a model name.",
    action: "edit",
    actionLabel: "Edit provider",
  },
  keyring_unavailable: {
    title: "Can't read the API key",
    hint: "The system keyring isn't responding. On KDE, unlock KWallet. On Windows, check Credential Manager.",
    action: "retry",
    actionLabel: "Test again",
  },
  other: {
    title: "The test didn't work",
    hint: "Something unexpected went wrong. Check the address, key and model.",
    action: "retry",
    actionLabel: "Test again",
  },
};

export const testCopy = (category: string): TestCopy =>
  COPY[category] ?? COPY.other;

const text = (e: unknown): string =>
  e instanceof Error ? e.message : String(e ?? "");

export const isKeyReentry = (e: unknown): boolean =>
  /enter the API key again|base URL changed/i.test(text(e));

/** The endpoint has no model list: the user types the model name instead. */
export const isListUnsupported = (e: unknown): boolean =>
  /does not list models/i.test(text(e));

export function saveErrorCopy(e: unknown): { title: string; body: string } {
  if (isKeyReentry(e))
    return {
      title: "Enter the API key again",
      body: "You changed the address, so re-enter the API key. It is never sent to a new address without you.",
    };
  const m = text(e);
  if (/keyring|keychain/i.test(m))
    return {
      title: "Can't store the API key",
      body: "The system keyring isn't responding. On KDE, unlock KWallet. On Windows, check Credential Manager. Nothing was changed.",
    };
  if (/invalid input/i.test(m))
    return {
      title: "Those settings don't look right",
      body: m.replace(/^.*invalid input:\s*/i, "") || "Check the fields.",
    };
  return {
    title: "Couldn't save the provider",
    body: "Nothing was changed. Try again.",
  };
}
