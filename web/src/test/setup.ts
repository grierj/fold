import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, beforeEach } from "vitest";
import { forgetSite } from "../lib/useSite";

// /site is shared for the page's life; each test starts with nothing read.
beforeEach(forgetSite);

afterEach(() => cleanup());
