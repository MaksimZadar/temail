import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../api", () => ({
  getStatus: vi.fn(),
  saveAccount: vi.fn(),
  probeCert: vi.fn(),
  trustCert: vi.fn(),
  testConnection: vi.fn(),
  syncStart: vi.fn(),
  syncStatus: vi.fn(),
  onEvent: vi.fn(() => () => {}),
}));

import * as api from "../api";
import { Wizard, initialStep } from "./wizard.svelte";

const m = vi.mocked(api);
const status = (o: Partial<api.SetupStatus> = {}): api.SetupStatus => ({
  configured: false,
  keyringOk: true,
  keyringMessage: "",
  bridgeReachable: true,
  certPinned: false,
  accountId: "",
  ...o,
});
const flush = () => new Promise((r) => setTimeout(r, 0));

beforeEach(() => vi.resetAllMocks());

describe("initialStep", () => {
  it("starts at checks until everything is in place", () => {
    expect(initialStep(status())).toBe("checks");
    expect(initialStep(status({ keyringOk: false, configured: true }))).toBe(
      "checks",
    );
    expect(
      initialStep(status({ bridgeReachable: false, configured: true })),
    ).toBe("checks");
  });
  it("resumes at the first unmet step", () => {
    expect(initialStep(status({ configured: true }))).toBe("cert");
    expect(initialStep(status({ configured: true, certPinned: true }))).toBe(
      "test",
    );
  });
});

describe("Wizard", () => {
  it("blocks continue while a check fails", async () => {
    m.getStatus.mockResolvedValue(status({ keyringOk: false }));
    const w = new Wizard(() => {});
    await w.start();
    expect(w.checksOk).toBe(false);
    w.primary();
    expect(w.step).toBe("checks");
  });

  it("Enter moves on from checks when they pass", async () => {
    m.getStatus.mockResolvedValue(status());
    const w = new Wizard(() => {});
    await w.start();
    w.primary();
    expect(w.step).toBe("account");
  });

  it("submits the account with the defaults and clears the password", async () => {
    m.getStatus.mockResolvedValue(status());
    m.saveAccount.mockResolvedValue(undefined);
    m.probeCert.mockResolvedValue({
      fingerprint: "AB12",
      subject: "127.0.0.1",
    });
    const w = new Wizard(() => {});
    await w.start();
    w.go("account");
    w.form.username = "me@proton.me";
    w.form.password = "bridge-pw";
    await w.submitAccount();
    expect(m.saveAccount).toHaveBeenCalledWith(
      "127.0.0.1",
      1143,
      1025,
      "starttls",
      "me@proton.me",
      "bridge-pw",
    );
    expect(w.form.password).toBe("");
    expect(w.step).toBe("cert");
    await flush();
    expect(w.probe?.fingerprint).toBe("AB12");
  });

  it("clears the password and shows friendly copy when saving fails", async () => {
    m.getStatus.mockResolvedValue(status());
    m.saveAccount.mockRejectedValue(
      new Error(
        "invalid input: the Bridge host must be 127.0.0.1, ::1 or localhost",
      ),
    );
    const w = new Wizard(() => {});
    await w.start();
    w.go("account");
    w.form.username = "u";
    w.form.password = "pw";
    await w.submitAccount();
    expect(w.form.password).toBe("");
    expect(w.step).toBe("account");
    expect(w.error?.title).toMatch(/host/i);
    expect(JSON.stringify(w.error)).not.toContain('pw"');
  });

  it("requires a password for a new account", async () => {
    m.getStatus.mockResolvedValue(status());
    const w = new Wizard(() => {});
    await w.start();
    w.go("account");
    w.form.username = "u";
    await w.submitAccount();
    expect(m.saveAccount).not.toHaveBeenCalled();
    expect(w.error).not.toBeNull();
  });

  it("trusts the probed fingerprint, then tests the connection", async () => {
    m.getStatus.mockResolvedValue(status({ configured: true }));
    m.probeCert.mockResolvedValue({ fingerprint: "AB12", subject: "x" });
    m.trustCert.mockResolvedValue(undefined);
    m.testConnection.mockResolvedValue({
      ok: true,
      category: "ok",
      message: "",
    });
    const w = new Wizard(() => {});
    await w.start();
    await flush();
    expect(w.step).toBe("cert");
    await w.trust();
    expect(m.trustCert).toHaveBeenCalledWith("AB12", false);
    await flush();
    expect(w.step).toBe("test");
    expect(w.tested).toBe(true);
  });

  it("offers a replace flow for already_pinned", async () => {
    m.getStatus.mockResolvedValue(status({ configured: true }));
    m.probeCert.mockResolvedValue({ fingerprint: "NEW", subject: "x" });
    m.trustCert.mockRejectedValueOnce(
      new Error(
        "already_pinned: this account already has a pinned Bridge certificate",
      ),
    );
    m.testConnection.mockResolvedValue({
      ok: true,
      category: "ok",
      message: "",
    });
    const w = new Wizard(() => {});
    await w.start();
    await flush();
    await w.trust();
    expect(w.trustFailure).toBe("already_pinned");
    m.trustCert.mockResolvedValueOnce(undefined);
    w.recover();
    await flush();
    expect(m.trustCert).toHaveBeenLastCalledWith("NEW", true);
    expect(w.step).toBe("test");
  });

  it("maps a failed test to friendly copy and a working recovery", async () => {
    m.getStatus.mockResolvedValue(
      status({ configured: true, certPinned: true }),
    );
    m.testConnection.mockResolvedValueOnce({
      ok: false,
      category: "auth_failed",
      message: "x",
    });
    const w = new Wizard(() => {});
    await w.start();
    await flush();
    expect(w.step).toBe("test");
    expect(w.error?.title).toMatch(/password/i);
    w.recover();
    expect(w.step).toBe("account");
  });

  it("retries the test for bridge_unreachable", async () => {
    m.getStatus.mockResolvedValue(
      status({ configured: true, certPinned: true }),
    );
    m.testConnection.mockResolvedValueOnce({
      ok: false,
      category: "bridge_unreachable",
      message: "",
    });
    const w = new Wizard(() => {});
    await w.start();
    await flush();
    m.testConnection.mockResolvedValueOnce({
      ok: true,
      category: "ok",
      message: "",
    });
    w.recover();
    await flush();
    expect(w.tested).toBe(true);
    expect(w.error).toBeNull();
  });

  it("finish calls the callback", () => {
    const done = vi.fn();
    new Wizard(done).finish();
    expect(done).toHaveBeenCalled();
  });
});
