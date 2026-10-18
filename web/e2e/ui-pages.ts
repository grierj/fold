import { expect, type APIRequestContext, type Locator, type Page } from "@playwright/test";
import { apiAs } from "./helpers";

// The pages the UI checks cover (a11y.spec.ts, layout.spec.ts). Each has the
// person who opens it and a handle that shows the page has loaded.
export type UIPage = { id: string; who: string | null; path: string; ready: (p: Page) => Locator };

export async function uiPages(request: APIRequestContext): Promise<UIPage[]> {
  const admin = await apiAs(request, "grier");
  const { groups } = (await admin("GET", "/groups")) as { groups: { id: number; name: string }[] };
  const room = groups.find((g) => g.name === "Room 201 class")!;
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
  const heading = (name: string) => (p: Page) => p.getByRole("heading", { name });
  return [
    { id: "home-signed-out", who: null, path: "/", ready: (p) => p.getByText("Room 201 class").first() },
    { id: "login", who: null, path: "/login", ready: heading("Log in to Fold") },
    { id: "join", who: null, path: "/join", ready: (p) => p.locator("main") },
    { id: "home", who: "sam", path: "/", ready: (p) => p.getByText("Room 201 class").first() },
    { id: "my-groups", who: "sam", path: "/my-groups", ready: (p) => p.getByText("Room 201 class").first() },
    { id: "group-member", who: "sam", path: `/groups/${room.id}`, ready: heading("Room 201 class") },
    { id: "event", who: "sam", path: `/events/${event!.id}`, ready: heading("UI check night") },
    { id: "profile", who: "sam", path: "/profile", ready: (p) => p.getByLabel("Time zone for emails") },
    { id: "group-leader", who: "lee", path: `/groups/${room.id}`, ready: heading("Room 201 class") },
    { id: "event-leader", who: "lee", path: `/events/${event!.id}`, ready: heading("UI check night") },
    { id: "schedule-event", who: "lee", path: `/groups/${room.id}/events/new`, ready: (p) => p.getByLabel("Time zone").first() },
    { id: "admin", who: "grier", path: "/admin", ready: heading("Admin") },
    { id: "admin-users", who: "grier", path: "/admin?tab=users", ready: (p) => p.getByRole("button", { name: "New user" }) },
    { id: "new-group", who: "grier", path: "/groups/new", ready: (p) => p.getByLabel("Group name") },
    { id: "church-event", who: "grier", path: "/events/new", ready: (p) => p.getByLabel("Time zone").first() },
    { id: "not-found", who: "sam", path: "/no-such-page", ready: heading("Page not found") },
  ];
}

export async function open(page: Page, ui: UIPage) {
  await page.goto(ui.path);
  await expect(ui.ready(page)).toBeVisible();
  await page.waitForLoadState("networkidle");
}
