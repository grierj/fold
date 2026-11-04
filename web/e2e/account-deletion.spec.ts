import AxeBuilder from "@axe-core/playwright";
import { mkdirSync } from "node:fs";
import { expect, test, type Locator, type Page, type Route } from "@playwright/test";
import { apiAs, email, fromMore, gotoReady, nextFrames, PASSWORD, signInTo } from "./helpers";
// @ts-expect-error plain JS module shared with verify-ui
import { measure } from "./ui-measure.mjs";

// Deleting an account that is the only host of a church-wide event needs a
// replacement host, chosen by the admin (M7.4, R4-H, owner decision 3). Every
// scenario makes its own people and events through the API, so this spec
// depends on no other spec and no seed user besides the admin.

const adminName = "grier";
const proof = process.env.SCREENS_DIR;
const WIDTHS = [390, 768, 1280] as const;

const futureDate = (days: number) => new Date(Date.now() + days * 86_400_000).toISOString().slice(0, 10);

function recordOf(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw new Error("expected a JSON object");
  return Object.fromEntries(Object.entries(value));
}

function numberAt(value: unknown, ...path: string[]): number {
  let at: unknown = value;
  for (const key of path) at = recordOf(at)[key];
  if (typeof at !== "number") throw new Error(`expected a number at ${path.join(".")}`);
  return at;
}

type Admin = Awaited<ReturnType<typeof apiAs>>;
type Person = { id: number; email: string; name: string };

async function makePerson(admin: Admin, login: string, name: string): Promise<Person> {
  const made = await admin("POST", "/admin/users", { ageBand: "adult", name, email: email(login), password: PASSWORD, role: "member" });
  return { id: numberAt(made, "user", "id"), email: email(login), name };
}

// A church-wide event that `host` really hosts alone: the creator is always a
// host too, so the admin's own row is removed once the target has one.
async function soleHostedEvent(admin: Admin, adminId: number, host: Person, title: string, days: number): Promise<number> {
  const made = await admin("POST", "/events", {
    title, startsLocal: `${futureDate(days)}T18:00`, durationMinutes: 60, tzid: "UTC", location: "Hall",
    visibility: "public", hosts: [host.email], recurrenceRule: "",
  });
  const id = numberAt(made, "event", "id");
  await admin("DELETE", `/events/${id}/hosts/${adminId}`);
  return id;
}

// Intercept-and-modify (departure.spec.ts): fetch the real response, change
// one field of the parsed JSON, and fulfil the complete body with the original
// status and headers. A response that is not the expected real one is passed
// through untouched and recorded, so the spec fails on `problems`, not on a
// hang inside the handler.
async function modifyJSON(page: Page, pathname: string, edit: (body: Record<string, unknown>) => void) {
  const state = { fulfilled: 0, problems: [] as string[] };
  const matches = (url: URL) => url.pathname === pathname;
  const handler = async (route: Route) => {
    if (route.request().method() !== "GET") return route.continue();
    const response = await route.fetch();
    try {
      if (response.status() !== 200) throw new Error(`${pathname}: status ${response.status()}`);
      const body = recordOf(await response.json());
      edit(body);
      state.fulfilled++;
      await route.fulfill({ response, json: body });
    } catch (e) {
      state.problems.push(String(e));
      await route.fulfill({ response });
    }
  };
  await page.route(matches, handler);
  return { state, stop: () => page.unroute(matches, handler) };
}

async function openDelete(page: Page, target: Person): Promise<Locator> {
  await page.getByRole("searchbox", { name: "Search by name or email" }).fill(target.email);
  const row = page.getByRole("row").filter({ hasText: target.email });
  await fromMore(row, "Delete");
  return row;
}

// Picks a host the way a keyboard user does: focus the closed select, then press
// ArrowDown or ArrowUp until the wanted person is selected. How many presses it
// takes is worked out from the real options (disabled ones are skipped by the
// browser), so other people in the list cannot change the outcome. The caller
// asserts the chosen value and what follows from it.
async function chooseHost(page: Page, row: Locator, title: string, name: string) {
  const select = row.getByLabel(`New host for ${title}`, { exact: true });
  await select.focus();
  await expect(select).toBeFocused();
  const steps = await select.evaluate((el, wanted) => {
    if (!(el instanceof HTMLSelectElement)) throw new Error("expected a select");
    const enabled = [...el.options].filter((o) => !o.disabled);
    const at = enabled.findIndex((o) => o.selected);
    const to = enabled.findIndex((o) => o.textContent?.trim() === wanted);
    if (to < 0) throw new Error(`no option for ${wanted}`);
    return to - at;
  }, name);
  expect(steps, `${name} is not already chosen`).not.toBe(0);
  for (let i = 0; i < Math.abs(steps); i++) await page.keyboard.press(steps > 0 ? "ArrowDown" : "ArrowUp");
  await expect(select.locator("option:checked")).toHaveText(name);
}

// One state, at 390, 768 and 1280: axe and layout checks first, then the
// screenshot when SCREENS_DIR asks for one. Call it only after the state's own
// assertions have passed.
async function evidence(page: Page, state: string) {
  for (const width of WIDTHS) {
    await page.setViewportSize({ width, height: 900 });
    await page.evaluate(() => window.scrollTo(0, 0));
    await nextFrames(page);
    if (width >= 768 && (state === "panel" || state === "stale")) {
      for (const label of [page.getByRole("columnheader", { name: "Actions" }), page.getByText("Member", { exact: true })]) {
        const lines = await label.evaluate((element) => {
          const range = document.createRange();
          range.selectNodeContents(element);
          return new Set([...range.getClientRects()].filter((rect) => rect.width > 0 && rect.height > 0).map((rect) => Math.round(rect.top))).size;
        });
        expect(lines, `${state} at ${width}px: ${await label.textContent()} wraps`).toBe(1);
      }
      // Keeping those labels whole must not push the page sideways.
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      expect(overflow, `${state} at ${width}px: sideways overflow`).toBeLessThanOrEqual(0);
    }
    if (width !== 768) {
      const wcag = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
      const headings = await new AxeBuilder({ page }).withRules(["heading-order"]).analyze();
      const bad = [...wcag.violations, ...headings.violations].filter((v) => v.impact === "moderate" || v.impact === "serious" || v.impact === "critical");
      expect(bad.map((v) => `${v.impact} ${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`), `${state} at ${width}px`).toEqual([]);
    }
    if (width === 390) {
      const layout = await page.evaluate(measure);
      expect(layout.overflow, `${state}: sideways overflow`).toBeLessThanOrEqual(0);
      expect(layout.clipped, `${state}: clipped select`).toEqual([]);
      expect(layout.under24, `${state}: targets under 24px`).toEqual([]);
    }
    if (proof) {
      mkdirSync(proof, { recursive: true });
      await page.screenshot({ path: `${proof}/account-deletion-${state}-${width}.png`, fullPage: true, animations: "disabled" });
    }
  }
}

test.describe("replacement host before an account is deleted", () => {
  test.use({ locale: "en-US", timezoneId: "America/Boise" });
  test.setTimeout(120_000);

  test("the admin chooses a new host for each event, and the event page lists them afterward", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const admin = await apiAs(request, adminName);
    const adminId = numberAt(await admin("GET", "/auth/me"), "user", "id");
    const target = await makePerson(admin, `hana-host-${suffix}`, `Hana Host ${suffix}`);
    const rafa = await makePerson(admin, `rafa-replacement-${suffix}`, `Rafa Replacement ${suffix}`);
    const title = `Easter breakfast ${suffix}`;
    const eventId = await soleHostedEvent(admin, adminId, target, title, 20);

    await signInTo(page, adminName, "/admin?tab=users");
    const row = await openDelete(page, target);
    await expect(row.getByText("They are the only host of these church-wide events. Choose who hosts each one next:")).toBeVisible();
    const select = row.getByLabel(`New host for ${title}`, { exact: true });
    await expect(select).toHaveValue("");
    await expect(row.getByText(/^Next date: /)).toBeVisible();
    await expect(row.getByText("Choose a new host for 1 more event.")).toBeVisible();
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    await evidence(page, "panel");

    await chooseHost(page, row, title, rafa.name);
    await expect(select).toHaveValue(String(rafa.id));
    await expect(row.getByRole("button", { name: "Delete account" })).toBeEnabled();
    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(page.getByRole("row").filter({ hasText: target.email })).toHaveCount(0);

    await gotoReady(page, `/events/${eventId}`);
    await expect(page.getByRole("heading", { name: title, level: 1 })).toBeVisible();
    await expect(page.getByText(rafa.email)).toBeVisible();
    await expect(page.getByText(target.email)).toHaveCount(0);
    await expect(page.getByText("No one hosts this event right now. Add a host below.")).toHaveCount(0);
  });

  test("a list that changed while the panel was open keeps the first pick and asks for the new event", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const admin = await apiAs(request, adminName);
    const adminId = numberAt(await admin("GET", "/auth/me"), "user", "id");
    const target = await makePerson(admin, `hana-stale-${suffix}`, `Hana Host ${suffix} stale`);
    const rafa = await makePerson(admin, `rafa-stale-${suffix}`, `Rafa Replacement ${suffix}`);
    const first = `Easter breakfast ${suffix}`;
    const second = `Summer picnic ${suffix}`;
    const firstId = await soleHostedEvent(admin, adminId, target, first, 21);

    await signInTo(page, adminName, "/admin?tab=users");
    const row = await openDelete(page, target);
    await expect(row.getByLabel(`New host for ${first}`, { exact: true })).toBeVisible();
    // While the panel is open, another event appears that they also host alone.
    const secondId = await soleHostedEvent(admin, adminId, target, second, 22);

    await chooseHost(page, row, first, rafa.name);
    await expect(row.getByLabel(`New host for ${first}`, { exact: true })).toHaveValue(String(rafa.id));
    await expect(row.getByRole("button", { name: "Delete account" })).toBeEnabled();
    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByRole("alert")).toContainText(/still need a new host/);
    const newSelect = row.getByLabel(`New host for ${second}`, { exact: true });
    await expect(newSelect).toBeVisible();
    await expect(newSelect).toHaveValue("");
    await expect(newSelect).toBeFocused();
    await expect(row.getByLabel(`New host for ${first}`, { exact: true })).toHaveValue(String(rafa.id));
    await expect(row.getByText("Choose a new host for 1 more event.")).toBeVisible();
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    await evidence(page, "stale");

    await chooseHost(page, row, second, rafa.name);
    await expect(newSelect).toHaveValue(String(rafa.id));
    await expect(row.getByRole("button", { name: "Delete account" })).toBeEnabled();
    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(page.getByRole("row").filter({ hasText: target.email })).toHaveCount(0);

    for (const id of [firstId, secondId]) {
      await gotoReady(page, `/events/${id}`);
      await expect(page.getByText(rafa.email)).toBeVisible();
      await expect(page.getByText(target.email)).toHaveCount(0);
    }
  });

  test("with no one to hand an event to, the panel says so calmly and deletes nothing", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const admin = await apiAs(request, adminName);
    const adminId = numberAt(await admin("GET", "/auth/me"), "user", "id");
    const target = await makePerson(admin, `hana-alone-${suffix}`, `Hana Host ${suffix} alone`);
    const title = `Harvest supper ${suffix}`;
    await soleHostedEvent(admin, adminId, target, title, 23);

    await signInTo(page, adminName, "/admin?tab=users");
    // The real list has candidates; the only change is that there are none.
    const fixture = await modifyJSON(page, `/api/v1/admin/users/${target.id}/sole-host-events`, (body) => {
      if (!Array.isArray(body.events) || body.events.length < 1) throw new Error("the real list has no events");
      body.candidates = [];
    });
    const row = await openDelete(page, target);
    await expect(row.getByText(title)).toBeVisible();
    const line = row.getByText("No one else can host these events yet. Add or enable an adult account, then come back.");
    await expect(line).toBeVisible();
    await expect(line).not.toHaveAttribute("role", "alert");
    await expect(row.getByRole("alert")).toHaveCount(0);
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    expect(fixture.state.problems).toEqual([]);
    expect(fixture.state.fulfilled).toBeGreaterThanOrEqual(1);
    await evidence(page, "no-candidates");

    await fixture.stop();
    await row.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByRole("row").filter({ hasText: target.email })).toBeVisible();
  });

  test("an event with no host shows a calm line and the Add host form", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const admin = await apiAs(request, adminName);
    const title = `Hostless gathering ${suffix}`;
    const made = await admin("POST", "/events", {
      title, startsLocal: `${futureDate(24)}T18:00`, durationMinutes: 60, tzid: "UTC", location: "Hall",
      visibility: "public", hosts: [], recurrenceRule: "",
    });
    const eventId = numberAt(made, "event", "id");

    await signInTo(page, adminName, "/admin");
    // The real event has its creator as host; the only change is an empty host list.
    const fixture = await modifyJSON(page, `/api/v1/events/${eventId}`, (body) => {
      const event = recordOf(body.event);
      if (event.churchWide !== true || !Array.isArray(event.hosts)) throw new Error("not a church-wide event with a host list");
      event.hosts = [];
      body.event = event;
    });
    await gotoReady(page, `/events/${eventId}`);
    await expect(page.getByRole("heading", { name: title, level: 1 })).toBeVisible();
    const line = page.getByText("No one hosts this event right now. Add a host below.");
    await expect(line).toBeVisible();
    await expect(line).not.toHaveAttribute("role", "alert");
    await expect(page.getByRole("alert").filter({ hasText: "No one hosts" })).toHaveCount(0);
    await expect(page.getByLabel("Host email")).toBeVisible();
    expect(fixture.state.problems).toEqual([]);
    expect(fixture.state.fulfilled).toBeGreaterThanOrEqual(1);
    await evidence(page, "host-needed");
    await fixture.stop();
  });
});
