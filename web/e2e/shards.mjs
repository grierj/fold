// Shard planning for e2e/run.sh. No dependencies beyond Node.
//
//   node e2e/shards.mjs plan <n>        one line per shard: "<E2E_PARTS>|<spec files>"
//   node e2e/shards.mjs record <json>…  update durations.json from JSON reports
//
// A unit is a spec file, or one part of the page checks ("a11y:2",
// "layout:0", "a11y:rest"; see partSelected in ui-pages.ts). Units go,
// longest first, to the shard with the least work so far, using the
// seconds in durations.json (3 for a unit it has not seen). Stale numbers
// only make the shards less even; every unit still runs exactly once.
import { readdirSync, readFileSync, writeFileSync } from "node:fs";

const dir = new URL(".", import.meta.url);
const durationsFile = new URL("durations.json", dir);
const partedSpecs = { "a11y.spec.ts": "a11y", "layout.spec.ts": "layout" };
const uiParts = Number(/export const UI_PARTS = (\d+)/.exec(readFileSync(new URL("ui-pages.ts", dir), "utf8"))[1]);

function units() {
  const out = [];
  for (const file of readdirSync(dir).filter((f) => f.endsWith(".spec.ts")).sort()) {
    const spec = partedSpecs[file];
    if (!spec) {
      out.push({ id: file, file });
      continue;
    }
    for (let i = 0; i < uiParts; i++) out.push({ id: `${spec}:${i}`, file, part: `${spec}:${i}` });
    if (spec === "a11y") out.push({ id: "a11y:rest", file, part: "a11y:rest" });
  }
  return out;
}

function plan(n) {
  const seconds = JSON.parse(readFileSync(durationsFile, "utf8"));
  const shards = Array.from({ length: n }, () => ({ load: 0, files: new Set(), parts: [] }));
  const weighted = units().map((u) => ({ ...u, s: seconds[u.id] ?? 3 }));
  weighted.sort((a, b) => b.s - a.s || a.id.localeCompare(b.id));
  for (const u of weighted) {
    const shard = shards.reduce((min, s) => (s.load < min.load ? s : min));
    shard.load += u.s;
    shard.files.add(u.file);
    if (u.part) shard.parts.push(u.part);
  }
  for (const s of shards) console.log(`${s.parts.join(" ")}|${[...s.files].sort().join(" ")}`);
}

// The unit a test belongs to, from its file and title.
function unitOf(file, title) {
  const spec = partedSpecs[file];
  if (!spec) return file;
  const m = /\(part (\d+) of \d+\)/.exec(title);
  return m ? `${spec}:${Number(m[1]) - 1}` : `${spec}:rest`;
}

function record(reports) {
  const seconds = JSON.parse(readFileSync(durationsFile, "utf8"));
  const seen = {};
  const walk = (suite, file) => {
    file = suite.file ?? file;
    for (const spec of suite.specs ?? []) {
      if (!file.endsWith(".spec.ts")) continue;
      const id = unitOf(file.split("/").pop(), spec.title);
      for (const t of spec.tests) for (const r of t.results) seen[id] = (seen[id] ?? 0) + r.duration / 1000;
    }
    for (const child of suite.suites ?? []) walk(child, file);
  };
  for (const path of reports) for (const suite of JSON.parse(readFileSync(path, "utf8")).suites) walk(suite, "");
  for (const [id, s] of Object.entries(seen)) seconds[id] = Math.round(s * 10) / 10;
  const sorted = Object.fromEntries(Object.entries(seconds).sort(([a], [b]) => a.localeCompare(b)));
  writeFileSync(durationsFile, JSON.stringify(sorted, null, 2) + "\n");
}

const [cmd, ...args] = process.argv.slice(2);
if (cmd === "plan") plan(Number(args[0]));
else if (cmd === "record") record(args);
else {
  console.error("usage: node e2e/shards.mjs plan <n> | record <report.json>...");
  process.exit(2);
}
