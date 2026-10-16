import { describe, expect, it } from "vitest";
import { ApiError } from "../api";
import { ERROR_COPY, errorCode, errorText } from "./errors";

describe("errorText", () => {
  it("never shows the server's text", () => {
    const e = new ApiError(500, "boom", { error: "boom", code: "server_error" });
    expect(errorText(e)).toBe(ERROR_COPY.server_error);
    expect(errorText(e)).not.toContain("boom");
  });

  it("words a specific code, even when the page has its own fallback", () => {
    const e = new ApiError(409, "that email already has an account", { code: "email_taken" });
    expect(errorText(e, "We couldn't send the invitation.")).toBe(ERROR_COPY.email_taken);
    expect(errorCode(e)).toBe("email_taken");
  });

  it("uses the page's wording for general failures", () => {
    expect(errorText(new ApiError(500, "x", { code: "server_error" }), "We couldn't load the groups.")).toBe(
      "We couldn't load the groups. Please try again in a minute.",
    );
    expect(errorText(new ApiError(400, "x", { code: "invalid" }), "We couldn't save your changes.")).toBe(
      "We couldn't save your changes. Please try again.",
    );
  });

  it("treats anything that isn't an API error as a connection problem", () => {
    expect(errorText(new TypeError("Failed to fetch"))).toMatch(/couldn't reach Fold/);
    expect(errorText(new Error("boom"))).not.toContain("boom");
  });

  it("falls back to the general line for an unknown code", () => {
    expect(errorText(new ApiError(418, "teapot", { code: "teapot" }))).toBe(ERROR_COPY.server_error);
    expect(errorCode(new ApiError(418, "teapot", { code: "teapot" }))).toBe("");
  });
});
