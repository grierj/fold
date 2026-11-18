// Shard planning for e2e/run.sh. No dependencies beyond Node.
//
//   node e2e/shards.mjs plan <n>        one line per shard: "<E2E_PARTS>|<spec files>"
//   node e2e/shards.mjs record <json>…  update durations.json from JSON reports
//
// A unit is a spec file, or one part of a spec that runs in parts
// ("a11y:2", "layout:0", "a11y:rest", "reason-review:390"; see partSelected
// in ui-pages.ts). Units go, longest first, to the shard with the least work
// so far, using the seconds in durations.json. A unit it has no seconds for
// stops the plan: an unknown unit used to count as 3s, and a 300s spec hid
// there and set the length of every run.
import { readdirSync, readFileSync, writeFileSync } from "node:fs";

const dir = new URL(".", import.meta.url);
const durationsFile = new URL("durations.json", dir);
const uiParts = Number(/export const UI_PARTS = (\d+)/.exec(readFileSync(new URL("ui-pages.ts", dir), "utf8"))[1]);
const numbered = Array.from({ length: uiParts }, (_, i) => String(i));

// The specs that run in parts, and how a test's full title names its part.
const partedSpecs = {
  "a11y.spec.ts": { name: "a11y", parts: [...numbered, "rest"], partOf: (title) => numberedPart(title) ?? "rest" },
  "layout.spec.ts": { name: "layout", parts: numbered, partOf: (title) => numberedPart(title) ?? "rest" },
  "reason-review-session.spec.ts": { name: "reason-review", parts: ["390", "1280"], partOf: (title) => /\bat (\d+)px\b/.exec(title)?.[1] },
};

function numberedPart(title) {
  const m = /\(part (\d+) of \d+\)/.exec(title);

  return m ? String(Number(m[1]) - 1) : undefined;
}

function units() {
  const out = [];

  for (const file of readdirSync(dir).filter((f) => f.endsWith(".spec.ts")).sort()) {
    const spec = partedSpecs[file];

    if (!spec) {
      out.push({ id: file, file });
      continue;
    }

    for (const part of spec.parts) out.push({ id: `${spec.name}:${part}`, file, part: `${spec.name}:${part}` });
  }

  return out;
}

function plan(n) {
  const seconds = JSON.parse(readFileSync(durationsFile, "utf8"));
  const all = units();
  const unknown = all.filter((u) => !(u.id in seconds)).map((u) => u.id);

  if (unknown.length > 0) {
    console.error(
      `shards.mjs: e2e/durations.json has no seconds for ${unknown.join(", ")}.\n` +
        "Add an estimate for each, or record real ones with E2E_RECORD_DURATIONS=1 make e2e.",
    );
    process.exit(1);
  }

  const shards = Array.from({ length: n }, () => ({ load: 0, files: new Set(), parts: [] }));
  const weighted = all.map((u) => ({ ...u, s: seconds[u.id] }));
  weighted.sort((a, b) => b.s - a.s || a.id.localeCompare(b.id));

  for (const u of weighted) {
    const shard = shards.reduce((min, s) => (s.load < min.load ? s : min));
    shard.load += u.s;
    shard.files.add(u.file);

    if (u.part) shard.parts.push(u.part);
  }

  for (const s of shards) console.log(`${s.parts.join(" ")}|${[...s.files].sort().join(" ")}`);
}

// The unit a test belongs to, from its file and full title (describe titles included).
function unitOf(file, title) {
  const spec = partedSpecs[file];

  return spec ? `${spec.name}:${spec.partOf(title)}` : file;
}

function record(reports) {
  const seconds = JSON.parse(readFileSync(durationsFile, "utf8"));
  const seen = {};

  const walk = (suite, file, titles) => {
    file = suite.file ?? file;
    const path = suite.title && suite.title !== file && !suite.title.endsWith(".ts") ? [...titles, suite.title] : titles;

    for (const spec of suite.specs ?? []) {
      if (!file.endsWith(".spec.ts")) continue;
      const id = unitOf(file.split("/").pop(), [...path, spec.title].join(" › "));

      for (const t of spec.tests) for (const r of t.results) seen[id] = (seen[id] ?? 0) + r.duration / 1000;
    }

    for (const child of suite.suites ?? []) walk(child, file, path);
  };

  for (const path of reports) for (const suite of JSON.parse(readFileSync(path, "utf8")).suites) walk(suite, "", []);

  for (const [id, s] of Object.entries(seen)) seconds[id] = Math.round(s * 10) / 10;
  const known = new Set(units().map((u) => u.id));
  const sorted = Object.fromEntries(Object.entries(seconds).filter(([id]) => known.has(id)).sort(([a], [b]) => a.localeCompare(b)));
  writeFileSync(durationsFile, JSON.stringify(sorted, null, 2) + "\n");
}

const [cmd, ...args] = process.argv.slice(2);

if (cmd === "plan") plan(Number(args[0]));
else if (cmd === "record") record(args);
else {
  console.error("usage: node e2e/shards.mjs plan <n> | record <report.json>...");
  process.exit(2);
}
