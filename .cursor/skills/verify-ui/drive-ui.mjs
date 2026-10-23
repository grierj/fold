// verify-ui drive: seed a fresh verify-fold instance, then load the main
// pages as signed-out, member, leader and admin at 390/768/1280, saving a
// screenshot and running the UI checks for each. Writes results.json and
// result.txt under $VERIFY_EVIDENCE/ui/. Exit 1 when a hard check fails.
// Run only against an instance started by `verify-fold up`.
import { createRequire } from "node:module";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
// One measurement shared with web/e2e/layout.spec.ts.
import { measure } from "../../../web/e2e/ui-measure.mjs";

const require = createRequire(new URL("../../../web/package.json", import.meta.url));
const { chromium } = require("playwright");
const AxeBuilder = require("@axe-core/playwright").default;

const url = process.env.VERIFY_URL;
const evidence = process.env.VERIFY_EVIDENCE;
if (!url || !evidence) { console.error("drive-ui: eval \"$(verify-fold env RUN_ID)\" first"); process.exit(2); }
const only = (process.env.UI_PAGES || "").split(",").filter(Boolean); // e.g. UI_PAGES=admin,schedule-event
const widths = (process.env.UI_WIDTHS || "390,768,1280").split(",").map(Number);
const dir = path.join(evidence, "ui");
await mkdir(dir, { recursive: true });

const PASSWORD = "password1234";
const people = { admin: ["verify-admin@example.com", "Verify Admin"], leader: ["verify-leader@example.com", "Lee Leader"], member: ["verify-member@example.com", "Sam Member"] };

const browser = await chromium.launch();
const newPage = async () => {
  const ctx = await browser.newContext({ locale: "en-US", timezoneId: "America/Boise", viewport: { width: 1280, height: 900 } });
  const p = await ctx.newPage(); p.setDefaultTimeout(15_000); return p;
};
async function signIn(page, who) {
  await page.goto(url + "/login");
  await page.getByLabel("Email").fill(people[who][0]);
  await page.getByLabel("Password").fill(PASSWORD);
  await page.getByRole("button", { name: "Log in" }).click();
  await page.waitForURL((u) => !u.pathname.startsWith("/login"));
}
async function api(page, method, p, data) {
  const csrf = (await (await page.request.get(url + "/api/v1/auth/csrf")).json()).csrfToken;
  const r = await page.request.fetch(url + "/api/v1" + p, { method, data, headers: { "X-CSRF-Token": csrf } });
  if (!r.ok()) throw new Error(`${method} ${p}: ${r.status()} ${await r.text()}`);
  return r.json();
}

// ---- Seed (precondition, not proof): setup wizard through the real form,
// then people, a group and a weekly series through the admin's own API calls.
const seedPage = await newPage();
await seedPage.goto(url + "/");
let seed;
if (new URL(seedPage.url()).pathname === "/setup") {
  await seedPage.locator("[name=site_name]").fill("Fold verify");
  await seedPage.locator("[name=timezone]").fill("America/Boise");
  await seedPage.locator("[name=name]").fill(people.admin[1]);
  await seedPage.locator("[name=email]").fill(people.admin[0]);
  await seedPage.locator("[name=password]").fill(PASSWORD);
  await seedPage.locator("[name=password_confirm]").fill(PASSWORD);
  await seedPage.locator("[name=age_band]").check();
  await seedPage.getByRole("button", { name: "Create site" }).click();
  await seedPage.waitForURL((u) => u.pathname === "/login");
}
await signIn(seedPage, "admin");
{
  const { groups } = await api(seedPage, "GET", "/groups");
  let group = groups.find((g) => g.name === "Tuesday Bible Study");
  if (!group) {
    const ids = {};
    for (const who of ["leader", "member"]) {
      ids[who] = (await api(seedPage, "POST", "/admin/users", { ageBand: "adult", email: people[who][0], name: people[who][1], password: PASSWORD, role: "member" })).user.id;
    }
    group = (await api(seedPage, "POST", "/groups", { name: "Tuesday Bible Study", description: "Weekly study of Romans.", tags: ["bible-study", "adults"], imageUrl: "", location: "Fellowship Hall", visibility: "public", memberLimit: 12 })).group;
    await api(seedPage, "POST", `/admin/groups/${group.id}/members`, { userId: ids.leader, role: "leader" });
    await api(seedPage, "POST", `/admin/groups/${group.id}/members`, { userId: ids.member, role: "member" });
    await api(seedPage, "POST", "/groups", { name: "Youth Choir", description: "Ages 13 to 17.", tags: ["music", "youth"], imageUrl: "", location: "Choir room", visibility: "public", memberLimit: null });
  }
  const now = new Date(); const d = new Date(now.getTime() + 7 * 864e5).toISOString().slice(0, 10);
  const { events = [] } = await api(seedPage, "GET", `/groups/${group.id}/events`).catch(() => ({}));
  const event = events[0] ?? (await api(seedPage, "POST", `/groups/${group.id}/events`, { title: "Romans 8", startsLocal: `${d}T19:00`, durationMinutes: 90, tzid: "America/Boise", location: "Fellowship Hall", recurrenceRule: "FREQ=WEEKLY;COUNT=4" })).event;
  seed = { groupId: group.id, eventId: event.id };
}
await seedPage.context().close();

// ---- Pages to check: [id, role, path, ready locator]
const pages = [
  ["home-signed-out", null, "/", (p) => p.getByText("Tuesday Bible Study").first()],
  ["login", null, "/login", (p) => p.getByRole("heading", { name: "Log in to Fold" })],
  ["join", null, "/join", (p) => p.locator("main")],
  ["home", "member", "/", (p) => p.getByText("Tuesday Bible Study").first()],
  ["my-groups", "member", "/my-groups", (p) => p.getByText("Tuesday Bible Study").first()],
  ["group-member", "member", `/groups/${seed.groupId}`, (p) => p.getByRole("heading", { name: "Tuesday Bible Study" })],
  ["event", "member", `/events/${seed.eventId}`, (p) => p.getByRole("heading", { name: "Romans 8" })],
  ["profile", "member", "/profile", (p) => p.getByLabel("Time zone for emails")],
  ["notifications", "member", "/notifications", (p) => p.getByRole("heading", { name: "Notifications" })],
  ["group-leader", "leader", `/groups/${seed.groupId}`, (p) => p.getByRole("heading", { name: "Tuesday Bible Study" })],
  ["schedule-event", "leader", `/groups/${seed.groupId}/events/new`, (p) => p.getByLabel("Time zone")],
  ["admin", "admin", "/admin", (p) => p.getByRole("heading", { name: "Admin" })],
  ["admin-users", "admin", "/admin?tab=users", (p) => p.getByRole("heading", { name: "Admin" })],
  ["new-group", "admin", "/groups/new", (p) => p.getByLabel("Group name")],
  ["church-event", "admin", "/events/new", (p) => p.getByLabel("Time zone")],
].filter(([id]) => !only.length || only.includes(id));

const results = []; let hard = 0;
const sessions = {};
for (const [id, role, p, ready] of pages) {
  if (!sessions[role ?? "anon"]) { const pg = await newPage(); if (role) await signIn(pg, role); sessions[role ?? "anon"] = pg; }
  const page = sessions[role ?? "anon"];
  for (const w of widths) {
    await page.setViewportSize({ width: w, height: 900 });
    await page.goto(url + p); await ready(page).waitFor(); await page.waitForLoadState("networkidle");
    const shot = `${id}-${w}.png`;
    await page.screenshot({ path: path.join(dir, shot), fullPage: true, animations: "disabled", caret: "hide" });
    const m = await page.evaluate(measure);
    const { violations } = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
    const axe = violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => `${v.id} (${v.nodes.length})`);
    const fails = [m.overflow > 0 && `horizontal overflow ${m.overflow}px`, m.cutOff.length && `cut off ${m.cutOff.map((c) => `${c.el} "${c.name}"`).join("; ")}`, m.clipped.length && `clipped select ${m.clipped.map((c) => `#${c.id} "${c.label}" ${c.text}>${c.room}px`).join("; ")}`,
      m.under24.length && `targets under 24px: ${m.under24.map((t) => `${t.el} "${t.name}" ${t.w}x${t.h}`).join("; ")}`, axe.length && `axe ${axe.join(", ")}`].filter(Boolean);
    hard += fails.length;
    results.push({ id, role, path: p, width: w, shot, fails, under44: w === 390 ? m.under44 : undefined });
    console.log(`${fails.length ? "FAIL" : "ok  "} ${id}@${w}${fails.length ? "  " + fails.join(" | ") : ""}${w === 390 ? `  (${m.under44.length} controls under 44px)` : ""}`);
  }
}
await browser.close();
await writeFile(path.join(dir, "results.json"), JSON.stringify({ url, seed, results }, null, 2));
const summary = results.map((r) => `${r.fails.length ? "FAIL" : "ok"} ${r.id}@${r.width} ${r.shot}${r.fails.length ? " — " + r.fails.join(" | ") : ""}${r.under44 ? ` [${r.under44.length} under 44px at 390]` : ""}`).join("\n");
await writeFile(path.join(dir, "result.txt"), `feature: ui-checks\nentry: verify-ui drive-ui.mjs, pages ${pages.map((x) => x[0]).join(",")}, widths ${widths}\nhard failures: ${hard}\n\n${summary}\n`);
process.exit(hard ? 1 : 0);
