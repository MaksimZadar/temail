import * as api from "../api";
import {
  classifyTrustError,
  friendlyCategory,
  friendlyProbeError,
  friendlySaveError,
  trustFailureCopy,
  type Friendly,
  type StepId,
  type TrustFailure,
} from "./errors";

export const STEPS: StepId[] = ["checks", "account", "cert", "test"];

export interface AccountForm {
  host: string;
  imapPort: number;
  smtpPort: number;
  tlsMode: "starttls" | "ssl";
  username: string;
  /** Held only until the submit call returns, then cleared. */
  password: string;
}

export const defaultForm = (): AccountForm => ({
  host: "127.0.0.1",
  imapPort: 1143,
  smtpPort: 1025,
  tlsMode: "starttls",
  username: "",
  password: "",
});

/** First step that still needs the user, given the current status. */
export function initialStep(s: api.SetupStatus): StepId {
  if (!s.keyringOk || !s.bridgeReachable || !s.configured) return "checks";
  return s.certPinned ? "test" : "cert";
}

export class Wizard {
  step = $state<StepId>("checks");
  status = $state<api.SetupStatus | null>(null);
  form = $state<AccountForm>(defaultForm());
  busy = $state(false);
  error = $state<Friendly | null>(null);
  probe = $state<api.CertProbe | null>(null);
  trustFailure = $state<TrustFailure | null>(null);
  tested = $state(false);

  constructor(private readonly onFinish: () => void) {}

  get index(): number {
    return STEPS.indexOf(this.step);
  }
  get checksOk(): boolean {
    return (
      !!this.status && this.status.keyringOk && this.status.bridgeReachable
    );
  }

  go(step: StepId): void {
    this.step = step;
    this.error = null;
    this.trustFailure = null;
    if (step === "cert") void this.loadProbe();
    if (step === "test") void this.runTest();
  }

  back(): void {
    if (this.busy || this.index === 0) return;
    // Going back from the test step lands on settings, not the cert dialog.
    this.go(STEPS[this.index === 3 ? 1 : this.index - 1]);
  }

  async refreshChecks(): Promise<void> {
    this.busy = true;
    this.error = null;
    try {
      this.status = await api.getStatus();
    } catch {
      this.error = {
        title: "Couldn't read the setup state",
        body: "Try again in a moment.",
        action: { label: "Try again", to: "retry" },
      };
    } finally {
      this.busy = false;
    }
  }

  async start(): Promise<void> {
    await this.refreshChecks();
    if (this.status) this.step = initialStep(this.status);
    if (this.step === "cert") void this.loadProbe();
    if (this.step === "test") void this.runTest();
  }

  /** Enter key / primary action for steps where that is safe. */
  primary(): void {
    if (this.busy) return;
    if (this.step === "checks" && this.checksOk) this.go("account");
    // The account form submits itself. The cert step needs a deliberate click.
  }

  async submitAccount(): Promise<void> {
    if (this.busy) return;
    const f = this.form;
    const keepPassword = f.password === "" && !!this.status?.configured;
    if (f.username.trim() === "" || (f.password === "" && !keepPassword)) {
      this.error = {
        title: "Missing details",
        body: "Enter your Bridge username and password.",
        action: { label: "OK", to: "account" },
      };
      return;
    }
    this.busy = true;
    this.error = null;
    try {
      await api.saveAccount(
        f.host.trim(),
        f.imapPort,
        f.smtpPort,
        f.tlsMode,
        f.username.trim(),
        f.password,
      );
    } catch (e) {
      this.error = friendlySaveError(e);
      return;
    } finally {
      f.password = "";
      this.busy = false;
    }
    this.status = this.status
      ? { ...this.status, configured: true }
      : this.status;
    this.go("cert");
  }

  async loadProbe(): Promise<void> {
    this.busy = true;
    this.probe = null;
    this.error = null;
    try {
      this.probe = await api.probeCert();
    } catch {
      this.error = friendlyProbeError();
    } finally {
      this.busy = false;
    }
  }

  async trust(replace = false): Promise<void> {
    if (!this.probe || this.busy) return;
    this.busy = true;
    this.error = null;
    this.trustFailure = null;
    try {
      await api.trustCert(this.probe.fingerprint, replace);
    } catch (e) {
      this.trustFailure = classifyTrustError(e);
      this.error = trustFailureCopy[this.trustFailure];
      this.busy = false;
      return;
    }
    this.busy = false;
    this.go("test");
  }

  async runTest(): Promise<void> {
    this.busy = true;
    this.error = null;
    this.tested = false;
    try {
      const r = await api.testConnection();
      if (r.ok) this.tested = true;
      else this.error = friendlyCategory(r.category);
    } catch {
      this.error = friendlyCategory("other");
    } finally {
      this.busy = false;
    }
  }

  /** Run the recovery action of the current error. */
  recover(): void {
    const a = this.error?.action;
    if (!a) return;
    if (a.to === "retry") {
      if (this.trustFailure === "already_pinned") void this.trust(true);
      else if (this.step === "checks") void this.refreshChecks();
      else if (this.step === "cert") void this.loadProbe();
      else if (this.step === "test") void this.runTest();
      else void this.submitAccount();
      return;
    }
    if (a.to === "checks") {
      this.go("checks");
      void this.refreshChecks();
      return;
    }
    this.go(a.to);
  }

  finish(): void {
    this.onFinish();
  }
}
