import { beforeEach, describe, expect, it } from "vitest";
import { needsReload, needsRestoreReload, reloadStarted, reloadSucceeded, requireReload, requireRestoreReload } from "./deleteReload";

beforeEach(() => reloadSucceeded(reloadStarted()));

describe("the per-person reload requirement", () => {
  it("holds one person until a list load that began after it works", () => {
    requireReload(9);
    expect(needsReload(9)).toBe(true);
    expect(needsReload(10)).toBe(false);
    reloadSucceeded(reloadStarted());
    expect(needsReload(9)).toBe(false);
  });

  it("is not released by a load that began before it was recorded", () => {
    const started = reloadStarted();
    requireReload(9);
    reloadSucceeded(started);
    expect(needsReload(9)).toBe(true);
    reloadSucceeded(reloadStarted());
    expect(needsReload(9)).toBe(false);
  });

  it("is not released by doing nothing: a failed load never calls reloadSucceeded", () => {
    requireReload(9);
    reloadStarted();
    expect(needsReload(9)).toBe(true);
  });

  it("holds a restore for everyone until a later successful load, and ignores an earlier one", () => {
    const earlier = reloadStarted();
    requireRestoreReload();
    expect(needsRestoreReload()).toBe(true);
    expect(needsReload(9)).toBe(false); // it is not about one person
    reloadSucceeded(earlier);
    expect(needsRestoreReload()).toBe(true);
    reloadSucceeded(reloadStarted());
    expect(needsRestoreReload()).toBe(false);
  });

  it("keeps a person's requirement and the restore requirement apart", () => {
    requireReload(9);
    const between = reloadStarted();
    requireRestoreReload();
    reloadSucceeded(between);
    expect(needsReload(9)).toBe(false);
    expect(needsRestoreReload()).toBe(true);
  });
});
