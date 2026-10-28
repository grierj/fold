import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { writeFile } from "node:fs/promises";
import { apiAs, attemptPendingSignIn, email, fromMore, PASSWORD, signIn } from "./helpers";

async function capture(page: Page, name: string) {
  if (!process.env.SCREENS_DIR) return;
  for (const width of [390, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${name} overflow at ${width}`).toBe(true);
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.screenshot({ path: `${process.env.SCREENS_DIR}/${name}-${width}.png`, fullPage: true });
  }
}

async function refused({ client, method, path, data, status }: { client: APIRequestContext; method: string; path: string; data?: unknown; status: number }) {
  const { csrfToken } = await (await client.get("/api/v1/auth/csrf")).json();
  const response = await client.fetch(`/api/v1${path}`, { method, data, headers: { "X-CSRF-Token": csrfToken } });
  expect(response.status(), `${method} ${path}: ${await response.text()}`).toBe(status);
}

test("a consented teen joins general events with restricted roles, private identity, and a status-bound calendar feed", async ({ page, request, browser }) => {
  test.setTimeout(150_000);
  page.setDefaultTimeout(15_000);
  const admin = await apiAs(request, "grier");
  const teenEmail = email("acceptance-teen");
  const teenPhone = "2085550119";
  const teenName = "Casey General";
  const registrations: { eventId: number; status: string }[] = [];
  const visibility: { role: string; identityVisible: boolean; badge: string | null }[] = [];
  const { user: teen } = await admin("POST", "/admin/users", {
    name: "Casey General Full Name", displayName: teenName, email: teenEmail, phone: teenPhone, password: PASSWORD,
    ageBand: "teen", birthDate: `${new Date().getUTCFullYear() - 15}-01-01`,
  });
  const adults: Record<string, number> = {};
  for (const role of ["leader", "member", "host"]) {
    const { user } = await admin("POST", "/admin/users", { name: `Acceptance ${role}`, email: email(`acceptance-${role}`), password: PASSWORD, ageBand: "adult" });
    adults[role] = user.id;
  }
  const { group } = await admin("POST", "/groups", { name: "Community service", description: "Church members of all ages serve together.", visibility: "public", tags: ["service"] });
  await admin("POST", `/admin/groups/${group.id}/members`, { userId: adults.leader, role: "leader" });
  await admin("POST", `/admin/groups/${group.id}/members`, { userId: adults.member, role: "member" });
  const startsLocal = new Date(Date.now() + 7 * 86400_000).toISOString().slice(0, 10) + "T18:00";
  const details = { startsLocal, durationMinutes: 60, tzid: "America/Boise", capacity: null, waitlistEnabled: false };
  const { event } = await admin("POST", `/groups/${group.id}/events`, { ...details, title: "Community gardening" });
  const { event: churchEvent } = await admin("POST", "/events", { ...details, title: "Church community supper", visibility: "public", hosts: [email("acceptance-host")] });

  await attemptPendingSignIn(page, "acceptance-teen");
  expect((await page.request.get(`/api/v1/events/${event.id}`)).status()).toBe(200);
  await admin("POST", `/admin/users/${teen.id}/teen-consents`, { scope: "account", status: "granted", evidenceText: "Parent signed account consent at the church office" });
  await signIn(page, "acceptance-teen");
  expect((await page.request.get("/api/v1/auth/me")).status()).toBe(200);
  await page.goto(`/groups/${group.id}`);
  await page.getByRole("button", { name: "Join this group", exact: true }).click();
  await expect(page.getByRole("button", { name: "Leave group", exact: true })).toBeVisible();
  for (const occurrence of [event, churchEvent]) {
    await page.goto(`/events/${occurrence.id}`);
    await page.getByRole("button", { name: "Going", exact: true }).click();
    await expect(page.getByRole("button", { name: "Going", exact: true })).toHaveAttribute("aria-pressed", "true");
    const response = await page.request.get(`/api/v1/events/${occurrence.id}`);
    const status = (await response.json()).occurrences[0].myRegistration;
    expect(status).toBe("going");
    registrations.push({ eventId: occurrence.id, status });
  }
  await capture(page, "active-teen-general-event");
  await refused({ client: page.request, method: "POST", path: "/groups", data: { name: "Teen-led group", visibility: "public" }, status: 403 });
  await refused({ client: page.request, method: "POST", path: "/events", data: { ...details, title: "Teen-hosted event" }, status: 403 });
  await refused({ client: page.request, method: "POST", path: "/me/avatar", status: 403 });
  await refused({ client: page.request, method: "GET", path: "/admin/users", status: 403 });
  await refused({ client: request, method: "PUT", path: `/groups/${group.id}/members/${teen.id}/role`, data: { role: "leader" }, status: 400 });
  await refused({ client: request, method: "POST", path: `/events/${churchEvent.id}/hosts`, data: { email: teenEmail }, status: 400 });
  await refused({ client: request, method: "PUT", path: `/admin/users/${teen.id}`, data: { role: "admin" }, status: 400 });

  const contexts = [];
  try {
    for (const role of ["member", "leader", "host", "admin"]) {
      const context = await browser.newContext(); contexts.push(context);
      const viewer = await context.newPage();
      viewer.setDefaultTimeout(15_000);
      await signIn(viewer, role === "admin" ? "grier" : `acceptance-${role}`);
      const eventID = role === "host" ? churchEvent.id : event.id;
      const eventResponse = await viewer.request.get(`/api/v1/events/${eventID}`);
      const body = await eventResponse.json();
      const attendee = body.occurrences[0].attendees?.find((a: { userId?: number }) => a.userId === teen.id);
      visibility.push({ role, identityVisible: attendee !== undefined, badge: attendee?.accountType ?? null });
      if (role === "admin") expect(attendee).toMatchObject({ email: teenEmail, phone: teenPhone });
      else {
        expect(JSON.stringify(body)).not.toContain(teenEmail);
        expect(JSON.stringify(body)).not.toContain(teenPhone);
      }
      if (role === "member") expect(attendee).toBeUndefined();
      else {
        expect(attendee).toMatchObject({ name: teenName, accountType: "teen", status: "going" });
        if (role !== "admin") {
          expect(attendee).not.toHaveProperty("email");
          expect(attendee).not.toHaveProperty("phone");
        }
        expect(attendee).not.toHaveProperty("avatarUrl");
      }
      await viewer.goto(`/events/${eventID}`);
      if (role === "member") {
        await expect(viewer.getByText(teenName, { exact: true })).toHaveCount(0);
        await expect(viewer.getByText("Teen", { exact: true })).toHaveCount(0);
        await expect(viewer.getByText("Some people's names are private.", { exact: true })).toBeVisible();
      } else {
        await fromMore(viewer, "Manage sign-ups", /^More for \d{4}-/);
        const roster = viewer.getByRole("region", { name: "Manage sign-ups", exact: true });
        await expect(roster.getByText(teenName, { exact: true })).toBeVisible();
        await expect(roster.getByText("Teen", { exact: true })).toBeVisible();
      }
      if (role !== "host") {
        const roster = await (await viewer.request.get(`/api/v1/groups/${group.id}/members`)).json();
        const member = roster.members.find((m: { id: number }) => m.id === teen.id);
        if (role === "member") expect(member).toBeUndefined();
        else expect(member).toMatchObject({ name: teenName, accountType: "teen", avatarUrl: "", role: "member" });
        if (role === "admin") expect(member).toMatchObject({ email: teenEmail, phone: teenPhone });
        else {
          expect(JSON.stringify(roster)).not.toContain(teenEmail);
          expect(JSON.stringify(roster)).not.toContain(teenPhone);
        }
      }
      await capture(viewer, `active-teen-${role}-visibility`);
    }
  } finally { for (const context of contexts) await context.close(); }

  const { csrfToken } = await (await page.request.get("/api/v1/auth/csrf")).json();
  const response = await page.request.post("/api/v1/me/calendar-feed", { headers: { "X-CSRF-Token": csrfToken }, data: { scope: "mine" } });
  expect(response.ok()).toBe(true);
  const { calendarFeed } = await response.json();
  const feedPath = new URL(calendarFeed.url).pathname;
  const calendarStatuses: { stage: string; status: number }[] = [];
  async function checkFeed(stage: string, expected: number) {
    const response = await request.get(feedPath);
    calendarStatuses.push({ stage, status: response.status() });
    expect(response.status(), stage).toBe(expected);
    return response;
  }
  const firstFeed = await checkFeed("active with consent", 200);
  expect(await firstFeed.text()).toContain("Community gardening");
  await admin("POST", `/admin/users/${teen.id}/teen-consents`, { scope: "account", status: "revoked" });
  expect((await page.request.get("/api/v1/auth/me")).status()).toBe(401);
  await checkFeed("account consent revoked", 404);
  await admin("PUT", `/admin/users/${teen.id}`, { status: "active" });
  await checkFeed("enabled without consent, pending", 404);
  await admin("POST", `/admin/users/${teen.id}/teen-consents`, { scope: "account", status: "granted", evidenceText: "Parent renewed account consent" });
  await checkFeed("consent restored", 200);
  await admin("PUT", `/admin/users/${teen.id}`, { status: "disabled" });
  await checkFeed("administratively disabled", 404);
  await admin("POST", `/admin/users/${teen.id}/teen-consents`, { scope: "account", status: "granted", evidenceText: "Parent consent remains on file" });
  await checkFeed("consent grant preserves suspension", 404);
  await admin("PUT", `/admin/users/${teen.id}`, { status: "active" });
  await checkFeed("administrator enabled with consent", 200);
  if (process.env.SCREENS_DIR) await writeFile(`${process.env.SCREENS_DIR}/result.txt`, JSON.stringify({ feature: "teen-access-acceptance", entryPoints: ["Waiting for consent", "Join this group", "Going", "Manage sign-ups", "Calendar feed"], teenId: teen.id, groupId: group.id, registrations, visibility, calendarStatuses }, null, 2) + "\n");
});
