import { test } from "node:test";
import assert from "node:assert/strict";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { randomBytes } from "node:crypto";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { spawnSync } from "node:child_process";

const script = new URL("./bundle-size.mjs", import.meta.url);
// Sizes follow the budget, so raising or lowering it keeps this test meaningful:
// the entry alone fits, the entry plus its eager preload does not.
const budget = Number(/const BUDGET_GZIP_KB = (\d+);/.exec(readFileSync(script, "utf8"))[1]);
const kb = (share) => Math.round(budget * share) * 1024;

test("budget includes unique eager preloads but not lazy chunks", () => {
  const cwd = mkdtempSync(join(tmpdir(), "fold-bundle-budget-"));
  try {
    mkdirSync(join(cwd, "dist/assets"), { recursive: true });
    writeFileSync(join(cwd, "dist/assets/entry.js"), randomBytes(kb(0.7)));
    writeFileSync(join(cwd, "dist/assets/shared.js"), randomBytes(kb(0.4)));
    writeFileSync(join(cwd, "dist/assets/lazy.js"), randomBytes(kb(1.5)));
    const run = (links) => {
      writeFileSync(join(cwd, "dist/index.html"), `<script type="module" src="/assets/entry.js"></script>${links}`);
      return spawnSync(process.execPath, [script.pathname], { cwd, encoding: "utf8" });
    };
    const eager = run('<link rel="modulepreload" href="/assets/shared.js">');
    assert.equal(eager.status, 1, eager.stdout + eager.stderr);
    assert.match(eager.stdout, /2 eager chunks total/);
    const lazy = run('<link rel="stylesheet" href="/assets/lazy.js">');
    assert.equal(lazy.status, 0, lazy.stdout + lazy.stderr);
    const duplicate = run('<link href="/assets/entry.js" rel="modulepreload">');
    assert.equal(duplicate.status, 0, duplicate.stdout + duplicate.stderr);
    assert.match(duplicate.stdout, /1 eager chunks total/);
  } finally {
    rmSync(cwd, { recursive: true, force: true });
  }
});
