import { expect, type APIRequestContext, type Locator, type Page, type Request } from "@playwright/test";
import { apiAs } from "./helpers";

// The pages the UI checks cover (a11y.spec.ts, layout.spec.ts). Each has the
// person who opens it and a handle that shows the page has loaded.
export type UIPage = { id: string; who: string | null; path: string; ready: (p: Page) => Locator; prepare?: (p: Page) => Promise<void> };

export async function uiPages(request: APIRequestContext): Promise<UIPage[]> {
  const admin = await apiAs(request, "grier");
  const { groups } = (await admin("GET", "/groups")) as { groups: { id: number; name: string }[] };
  const room = groups.find((g) => g.name === "Room 201 class")!;
  const householdEvents = await admin("GET", `/groups/${room.id}/events`);
  const householdEvent = (householdEvents.events as { id: number; title: string }[]).find((e) => e.title === "Household check night")!;
  const lee = await apiAs(request, "lee");
  const { events = [] } = (await lee("GET", `/groups/${room.id}/events`)) as { events?: { id: number; title: string }[] };
  let event = events.find((e) => e.title === "UI check night");
  if (!event) {
    const d = new Date(Date.now() + 7 * 864e5).toISOString().slice(0, 10);
    ({ event } = (await lee("POST", `/groups/${room.id}/events`, {
      title: "UI check night", startsLocal: `${d}T19:00`, durationMinutes: 90, tzid: "America/Boise",
      location: "Room 201", recurrenceRule: "FREQ=WEEKLY;COUNT=4",
    })) as { event: { id: number; title: string } });
  }
  const heading = (name: string) => (p: Page) => p.getByRole("heading", { name, level: 1 });
  return [
    { id: "home-signed-out", who: null, path: "/", ready: (p) => p.getByText("Room 201 class").first() },
    { id: "login", who: null, path: "/login", ready: heading("Log in to Fold") },
    { id: "join", who: null, path: "/join", ready: (p) => p.locator("main") },
    { id: "home", who: "sam", path: "/", ready: (p) => p.getByText("Room 201 class").first() },
    { id: "my-groups", who: "sam", path: "/my-groups", ready: (p) => p.getByText("Room 201 class").first() },
    { id: "group-member", who: "sam", path: `/groups/${room.id}`, ready: heading("Room 201 class") },
    { id: "event", who: "household-parent", path: `/events/${householdEvent.id}`, ready: heading("Household check night") },
    { id: "profile", who: "household-parent", path: "/profile", ready: heading("Your profile") },
    { id: "profile-details", who: "household-parent", path: "/profile/details", ready: heading("Profile details") },
    { id: "profile-privacy", who: "household-parent", path: "/profile/privacy", ready: heading("Privacy") },
    { id: "profile-security-email", who: "household-parent", path: "/profile/security", ready: heading("Email and password") },
    { id: "profile-security-password", who: "household-parent", path: "/profile/security?tab=password", ready: heading("Email and password") },
    { id: "profile-calendar", who: "household-parent", path: "/profile/calendar", ready: heading("Calendar feed") },
    { id: "profile-household", who: "household-parent", path: "/profile/household", ready: heading("Household"), prepare: async (p) => { await p.getByRole("button", { name: "Rename" }).click(); await expect(p.getByRole("form", { name: "Edit household" })).toBeVisible(); } },
    { id: "profile-guardian", who: "household-parent", path: "/profile/guardian", ready: heading("Parent or guardian link") },
    { id: "profile-invite", who: "household-parent", path: "/profile/invite", ready: heading("Invite someone") },
    { id: "profile-requests", who: "household-parent", path: "/profile/requests", ready: heading("Requests") },
    { id: "group-leader", who: "lee", path: `/groups/${room.id}`, ready: heading("Room 201 class") },
    { id: "event-leader", who: "lee", path: `/events/${householdEvent.id}`, ready: heading("Household check night"), prepare: async (p) => { await p.getByRole("button", { name: /^More for/ }).first().click(); await p.getByRole("menuitem", { name: "Manage sign-ups" }).click(); await expect(p.getByRole("region", { name: "Manage sign-ups" })).toBeVisible(); } },
    { id: "schedule-event", who: "lee", path: `/groups/${room.id}/events/new`, ready: (p) => p.getByLabel("Time zone").first() },
    { id: "admin", who: "grier", path: "/admin", ready: heading("Admin") },
    { id: "admin-users", who: "grier", path: "/admin?tab=users", ready: (p) => p.getByRole("button", { name: "New user" }) },
    { id: "contact-history", who: "grier", path: "/admin/contact-history", ready: heading("Contact access history") },
    { id: "new-group", who: "grier", path: "/groups/new", ready: (p) => p.getByLabel("Group name") },
    { id: "church-event", who: "grier", path: "/events/new", ready: (p) => p.getByLabel("Time zone").first() },
    { id: "calendar-agenda", who: "sam", path: "/calendar", ready: heading("Calendar") },
    { id: "calendar-week", who: "sam", path: "/calendar?view=week", ready: (p) => p.getByRole("button", { name: "Next week" }) },
    { id: "calendar-month", who: "sam", path: "/calendar?view=month", ready: (p) => p.getByRole("table") },
    { id: "calendar-signed-out", who: null, path: "/calendar", ready: (p) => p.getByText("Showing public events.") },
    { id: "not-found", who: "sam", path: "/no-such-page", ready: heading("Page not found") },
  ];
}

export async function open(page: Page, ui: UIPage) {
  const quiet = trackRequests(page);
  try {
    await page.goto(ui.path);
    await expect(ui.ready(page)).toBeVisible();
    await quiet();
  } finally {
    quiet.stop();
  }
  await ui.prepare?.(page);
}

// The page's data and images have arrived once no request has been in
// flight for 150ms. Playwright's "networkidle" means the same with a fixed
// 500ms of quiet, which cost the page checks about a minute per run.
// Playwright sometimes reports neither the end nor the failure of a request
// the previous page had in flight when it navigated away, so a request still
// open after 2s (this server answers in milliseconds) no longer counts.
function trackRequests(page: Page) {
  const inFlight = new Map<Request, number>();
  let lastChange = Date.now();
  const start = (r: Request) => { inFlight.set(r, Date.now()); lastChange = Date.now(); };
  const end = (r: Request) => { inFlight.delete(r); lastChange = Date.now(); };
  page.on("request", start);
  page.on("requestfinished", end);
  page.on("requestfailed", end);
  const open = () => [...inFlight.values()].some((at) => Date.now() - at < 2_000);
  const quiet = async () => {
    await expect.poll(() => !open() && Date.now() - lastChange >= 150, { intervals: [25] }).toBe(true);
  };
  quiet.stop = () => {
    page.off("request", start);
    page.off("requestfinished", end);
    page.off("requestfailed", end);
  };
  return quiet;
}

// The page checks (a11y.spec.ts, layout.spec.ts) run in this many parts so
// e2e/run.sh can spread them across shards.
export const UI_PARTS = 4;

// One contiguous slice of the list, so each person's pages stay together and
// sign-ins stay few. The parts together cover every page exactly once.
export function uiPart<T>(list: T[], part: number): T[] {
  return list.slice(Math.floor((part * list.length) / UI_PARTS), Math.floor(((part + 1) * list.length) / UI_PARTS));
}

// e2e/run.sh hands each shard its share of the page checks in E2E_PARTS, as
// "<spec>:<part>" words ("a11y:0 layout:3 a11y:rest"; rest is the spec's
// tests outside the parts). Unset, as in a plain `npx playwright test`,
// every part runs.
export function partSelected(spec: string, part: number | "rest"): boolean {
  const parts = process.env.E2E_PARTS;
  return parts === undefined || parts.split(/\s+/).includes(`${spec}:${part}`);
}

// Opens path and waits until its data and images have arrived (see
// trackRequests), for specs that check every page they open.
export async function gotoSettled(page: Page, path: string) {
  const quiet = trackRequests(page);
  try {
    await page.goto(path);
    await quiet();
  } finally {
    quiet.stop();
  }
}
