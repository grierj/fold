import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { randomBytes } from "node:crypto";
import { spawnSync } from "node:child_process";

const script = new URL("./bundle-size.mjs", import.meta.url);

test("budget includes unique eager preloads but not lazy chunks", () => {
  const cwd = mkdtempSync(join(tmpdir(), "fold-bundle-budget-"));
  try {
    mkdirSync(join(cwd, "dist/assets"), { recursive: true });
    writeFileSync(join(cwd, "dist/assets/entry.js"), randomBytes(100 * 1024));
    writeFileSync(join(cwd, "dist/assets/shared.js"), randomBytes(50 * 1024));
    writeFileSync(join(cwd, "dist/assets/lazy.js"), randomBytes(200 * 1024));
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
