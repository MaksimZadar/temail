import { describe, expect, it } from "vitest";
import {
  classifyTrustError,
  friendlyCategory,
  friendlySaveError,
  trustFailureCopy,
} from "./errors";

// Every category TestConnection can return (docs/ARCHITECTURE.md §3.3).
const categories = [
  "not_configured",
  "no_password",
  "keyring_unavailable",
  "bridge_unreachable",
  "untrusted_cert",
  "cert_mismatch",
  "auth_failed",
  "invalid_config",
  "other",
];

describe("friendlyCategory", () => {
  it.each(categories)("%s has copy and a recovery action", (c) => {
    const f = friendlyCategory(c);
    expect(f.title).not.toBe("");
    expect(f.body).not.toBe("");
    expect(f.action.label).not.toBe("");
  });

  it("falls back to the generic copy for unknown values", () => {
    expect(friendlyCategory("brand_new")).toEqual(friendlyCategory("other"));
  });

  it("sends each category to a sensible step", () => {
    expect(friendlyCategory("auth_failed").action.to).toBe("account");
    expect(friendlyCategory("cert_mismatch").action.to).toBe("cert");
    expect(friendlyCategory("untrusted_cert").action.to).toBe("cert");
    expect(friendlyCategory("keyring_unavailable").action.to).toBe("checks");
    expect(friendlyCategory("bridge_unreachable").action.to).toBe("retry");
  });

  it("wrong password copy points at Mailbox details", () => {
    expect(friendlyCategory("auth_failed").body).toMatch(/Mailbox details/);
  });
});

describe("friendlySaveError", () => {
  it("explains a non-loopback host", () => {
    const f = friendlySaveError(
      new Error(
        "invalid input: the Bridge host must be 127.0.0.1, ::1 or localhost",
      ),
    );
    expect(f.title).toMatch(/host/i);
  });
  it("explains a missing password", () => {
    expect(
      friendlySaveError(new Error("no Bridge password is stored")).title,
    ).toMatch(/password/i);
  });
  it("never echoes unknown error text", () => {
    const f = friendlySaveError(new Error("secret hunter2 blew up"));
    expect(JSON.stringify(f)).not.toContain("hunter2");
  });
});

describe("trust errors", () => {
  it("classifies the service errors", () => {
    expect(
      classifyTrustError(new Error("already_pinned: this account ...")),
    ).toBe("already_pinned");
    expect(
      classifyTrustError(
        new Error("the Bridge certificate changed since it was probed; x"),
      ),
    ).toBe("fingerprint_changed");
    expect(
      classifyTrustError(
        new Error(
          "the Bridge account changed while the certificate was being checked",
        ),
      ),
    ).toBe("account_changed");
    expect(classifyTrustError("???")).toBe("other");
  });
  it("has copy for each", () => {
    for (const k of Object.keys(trustFailureCopy)) {
      expect(
        trustFailureCopy[k as keyof typeof trustFailureCopy].body,
      ).not.toBe("");
    }
  });
});
