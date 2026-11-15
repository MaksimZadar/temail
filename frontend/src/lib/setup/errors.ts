// Plain-language copy for every setup failure, plus the recovery action.

export type StepId = "checks" | "account" | "cert" | "test";

export interface Friendly {
  title: string;
  body: string;
  /** Where the recovery button goes. "retry" repeats the failed step. */
  action: { label: string; to: StepId | "retry" };
}

const BRIDGE_HINT =
  "Make sure Proton Bridge is open and signed in, then try again.";

const table: Record<string, Friendly> = {
  not_configured: {
    title: "No account saved yet",
    body: "Enter your Bridge details first.",
    action: { label: "Enter details", to: "account" },
  },
  no_password: {
    title: "No password saved",
    body: "Enter the Bridge password again. It is the one Bridge shows under Mailbox details.",
    action: { label: "Enter password", to: "account" },
  },
  keyring_unavailable: {
    title: "Can't reach the password store",
    body: "temail keeps your Bridge password in the system keyring, and it isn't responding. On KDE, unlock KWallet. On Windows, check Credential Manager.",
    action: { label: "Check again", to: "checks" },
  },
  bridge_unreachable: {
    title: "Can't reach Proton Bridge",
    body: BRIDGE_HINT,
    action: { label: "Try again", to: "retry" },
  },
  untrusted_cert: {
    title: "Certificate not trusted yet",
    body: "temail hasn't saved Bridge's certificate yet.",
    action: { label: "Review certificate", to: "cert" },
  },
  cert_mismatch: {
    title: "Bridge's certificate has changed",
    body: "It no longer matches the one you trusted. This happens after reinstalling Bridge. If you didn't expect it, stop here.",
    action: { label: "Review certificate", to: "cert" },
  },
  auth_failed: {
    title: "Bridge didn't accept that password",
    body: "Use the password from Bridge's Mailbox details, not your Proton password.",
    action: { label: "Enter password", to: "account" },
  },
  invalid_config: {
    title: "Those settings don't look right",
    body: "Check the host, ports and connection type.",
    action: { label: "Edit settings", to: "account" },
  },
  other: {
    title: "Couldn't connect",
    body: "Something unexpected went wrong.",
    action: { label: "Try again", to: "retry" },
  },
};

/** Friendly copy for a TestConnection category. Unknown values become "other". */
export function friendlyCategory(category: string): Friendly {
  return table[category] ?? table.other;
}

const text = (e: unknown): string =>
  e instanceof Error ? e.message : String(e ?? "");

/** Friendly copy for an error thrown by SaveAccount. */
export function friendlySaveError(e: unknown): Friendly {
  const m = text(e);
  const retry = { label: "Fix and retry", to: "account" as const };
  if (
    m.includes("no Bridge password") ||
    m.includes("enter the Bridge password")
  )
    return { ...table.no_password, action: retry };
  if (m.includes("loopback") || m.includes("must be 127.0.0.1"))
    return {
      title: "That host isn't allowed",
      body: "temail only connects to Bridge on this computer. Use 127.0.0.1.",
      action: retry,
    };
  if (
    m.includes("store Bridge password") ||
    m.toLowerCase().includes("keyring")
  )
    return {
      ...table.keyring_unavailable,
      action: { label: "Check again", to: "checks" },
    };
  if (m.includes("invalid input") || m.includes("invalid config"))
    return {
      ...table.invalid_config,
      body: "Check the host, ports and username.",
      action: retry,
    };
  return {
    title: "Couldn't save those settings",
    body: "Nothing was changed. Check the details and try again.",
    action: retry,
  };
}

/** Friendly copy for an error thrown by ProbeCert. */
export function friendlyProbeError(): Friendly {
  return table.bridge_unreachable;
}

export type TrustFailure =
  "already_pinned" | "fingerprint_changed" | "account_changed" | "other";

export function classifyTrustError(e: unknown): TrustFailure {
  const m = text(e);
  if (m.includes("already_pinned")) return "already_pinned";
  if (m.includes("certificate changed since it was probed"))
    return "fingerprint_changed";
  if (m.includes("account changed while")) return "account_changed";
  return "other";
}

export const trustFailureCopy: Record<TrustFailure, Friendly> = {
  already_pinned: {
    title: "A different certificate is already saved",
    body: "You trusted another certificate for this account before. Replace it only if you reinstalled Bridge or expected this.",
    action: { label: "Replace it", to: "retry" },
  },
  fingerprint_changed: {
    title: "The certificate changed while you were looking at it",
    body: "Nothing was saved. Check again, and only continue if you expected this.",
    action: { label: "Check again", to: "retry" },
  },
  account_changed: {
    title: "Your settings changed",
    body: "Nothing was saved. Go back and check them.",
    action: { label: "Back to settings", to: "account" },
  },
  other: {
    title: "Couldn't save the certificate",
    body: "Nothing was saved. Try again.",
    action: { label: "Try again", to: "retry" },
  },
};
