// Vitest setup: jest-dom matchers, and unmount after each test.
import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, beforeEach } from "vitest";

// /site is shared for the page's life; each test starts with nothing read.
// Imported inside the hook so a test file's vi.mock of the API applies to it.
beforeEach(async () => {
  const { forgetSite } = await import("../lib/useSite");
  forgetSite();
});
afterEach(() => cleanup());
