import { describe, expect, it } from "vitest";
import { ApiError } from "../api/core";
import { emptyEventOptions, eventOptionsBody, optionFieldOfError, validateEventOptions } from "./eventOptionValues";

describe("validateEventOptions", () => {
  it("passes the defaults", () => {
    expect(validateEventOptions(emptyEventOptions())).toBeNull();
  });

  it("reports the first problem in the order the section reads", () => {
    const v = emptyEventOptions();
    v.remote = { available: true, capacity: "x" };
    v.video = { provider: "link", url: "", reveal: "registered_only" };
    expect(validateEventOptions(v)?.field).toBe("video");
    v.seats = { capacity: "0", waitlist: false, guests: 0 };
    expect(validateEventOptions(v)?.field).toBe("seats");
    v.seats = { capacity: "", waitlist: false, guests: 0 };
    v.video = { provider: "none", url: "", reveal: "registered_only" };
    expect(validateEventOptions(v)?.field).toBe("remote");
    v.remote = { available: false, capacity: "" };
    v.childcare = { available: true, capacity: "0", ages: "", notes: "" };
    expect(validateEventOptions(v)).toMatchObject({ field: "childcare" });
  });
});

describe("optionFieldOfError", () => {
  it.each([
    ["invalid_capacity", "seats"], ["invalid_video_url", "video"], ["invalid_childcare_capacity", "childcare"], ["remote_capacity_zero", "remote"],
  ])("puts %s under %s", (code, field) => {
    expect(optionFieldOfError(new ApiError(400, "x", { code }))).toBe(field);
  });
  it("leaves other errors to the form", () => {
    expect(optionFieldOfError(new ApiError(400, "x", { code: "nope" }))).toBeNull();
    expect(optionFieldOfError(new Error("x"))).toBeNull();
  });
});

describe("eventOptionsBody", () => {
  it("carries every option in the wire form", () => {
    const v = emptyEventOptions();
    v.seats = { capacity: "12", waitlist: true, guests: 2 };
    v.video = { provider: "link", url: " https://x.test/m ", reveal: "always" };
    expect(eventOptionsBody(v)).toMatchObject({
      capacity: 12, waitlistEnabled: true, guestsAllowed: 2, videoProvider: "link", videoUrl: "https://x.test/m", videoReveal: "always",
      remoteAvailable: false, childcareAvailable: false, reminderEmailMinutes: 1440, reminderPush: true,
    });
  });
});
