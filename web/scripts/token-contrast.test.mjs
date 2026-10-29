import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const script = fileURLToPath(new URL("./token-contrast.mjs", import.meta.url));
const sourcePath = fileURLToPath(new URL("../src/index.css", import.meta.url));
const source = await readFile(sourcePath, "utf8");

async function check(css) {
  const dir = await mkdtemp(path.join(tmpdir(), "fold-token-contrast-"));
  try {
    const fixture = path.join(dir, "index.css");
    await writeFile(fixture, css);
    return spawnSync(process.execPath, [script, fixture], { encoding: "utf8" });
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

test("current button CSS passes the contrast CLI", async () => {
  const result = await check(source);
  assert.equal(result.status, 0, result.stdout + result.stderr);
});

test("rejects color transitions across enabled and disabled button pairs", async () => {
  const fixture = source
    .replace("text-primary-foreground hover:bg-primary-hover", "text-primary-foreground transition-colors hover:bg-primary-hover")
    .replace("text-danger-foreground hover:bg-danger-hover", "text-danger-foreground transition-all hover:bg-danger-hover");
  const result = await check(fixture);
  assert.equal(result.status, 1, result.stdout + result.stderr);
  assert.match(result.stdout, /FAIL \.btn-primary animates both disabled text and background/);
  assert.match(result.stdout, /FAIL \.btn-danger animates both disabled text and background/);
});
