import { describe, expect, it } from "vitest";
import { clearClientData } from "./auth";

describe("clearClientData", () => {
  it("empties local and session storage", () => {
    localStorage.setItem("fold-dismissed-notifications:7", "[]");
    sessionStorage.setItem("anything", "x");
    clearClientData();
    expect(localStorage.length + sessionStorage.length).toBe(0);
  });
});
