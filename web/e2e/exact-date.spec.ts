import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { apiAs, email, fromMore, PASSWORD, signIn } from "./helpers";
// @ts-expect-error plain JS measurement shared with verify-ui
import { measure } from "./ui-measure.mjs";
import type { EventOccurrence } from "../src/types";

const adminName = process.env.VERIFY_URL ? "verify-admin" : "grier";
const proof = process.env.DATE_PROOF_DIR;
const results: unknown[] = [];
function save(name: string, data: unknown) {
  if (!proof) return;
  mkdirSync(proof, { recursive: true }); writeFileSync(`${proof}/${name}.json`, JSON.stringify(data, null, 2));
}
async function capture(page: Page, name: string, widths = [390, 768, 1280]) {
  if (!proof) return;
  for (const width of widths) {
    await page.setViewportSize({ width, height: 900 }); await page.waitForLoadState("networkidle");
    await page.evaluate(() => window.scrollTo(0, 0));
    const layout = await page.evaluate(measure);
    const { violations } = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
    const axe = violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => v.id);
    mkdirSync(proof!, { recursive: true }); await page.screenshot({ path: `${proof}/${name}-${width}.png`, fullPage: true });
    const bottomClearance = width === 390 ? await page.evaluate(() => {
      const bar = document.querySelector('nav[aria-label="Tabs"]');
      if (!bar || bar.getBoundingClientRect().height === 0) return 0;
      window.scrollTo(0, document.documentElement.scrollHeight);
      let bottom = 0;
      for (const element of document.querySelectorAll("main *, footer *")) {
        const rect = element.getBoundingClientRect();
        if (rect.width > 0 && rect.height > 0 && getComputedStyle(element).position !== "fixed") bottom = Math.max(bottom, rect.bottom);
      }
      return Math.round(bottom - bar.getBoundingClientRect().top);
    }) : null;
    if (width === 390) await page.screenshot({ path: `${proof}/${name}-bottom-390.png` });
    results.push({ name, width, layout, axe, bottomClearance }); save("results", results);
    if (bottomClearance !== null) expect(bottomClearance).toBeLessThanOrEqual(1);
    expect(layout.overflow).toBe(0); expect(layout.cutOff).toEqual([]); expect(layout.clipped).toEqual([]); expect(layout.under24).toEqual([]); expect(axe).toEqual([]);
  }
}

for (const scenario of ["adult-decline", "adult-block", "teen-removal"] as const) {
  test(`exact date ${scenario}: Ask → notice → Just mine with no parent authority, other accepted date retained`, async ({ page, request, browser }) => {
    test.setTimeout(180_000);
    const teen = scenario === "teen-removal";
    const name = `Exact ${scenario}`;
    const login = `exact-${scenario}`;
    const admin = await apiAs(request, adminName);
    const { user } = await admin("POST", "/admin/users", { name, email: email(login), password: PASSWORD, ageBand: teen ? "teen" : "adult", ...(teen ? { birthDate: `${new Date().getUTCFullYear() - 15}-01-01` } : {}) });
    if (teen) await admin("POST", `/admin/users/${user.id}/teen-consents`, { scope: "account", status: "granted", evidenceText: "Parent signed account consent" });
    const { group } = await admin("POST", "/groups", { name: `Hidden organizer ${scenario}`, visibility: "invite_only", tags: [] });
    const date = new Date(Date.now() + 7 * 86400_000).toISOString().slice(0, 10);
    const title = `Three hidden dates ${scenario}`;
    const { event } = await admin("POST", `/groups/${group.id}/events`, { title, location: "Invited date room", startsLocal: `${date}T18:00`, durationMinutes: 60, tzid: "UTC", recurrenceRule: "FREQ=WEEKLY;COUNT=3", remoteAvailable: true, remoteCapacity: 10, capacity: 10, guestsAllowed: 2, childcareAvailable: true, childcareCapacity: 10, videoProvider: "link", videoUrl: "https://example.com/exact-private-call", videoReveal: "registered_only" });
    const { occurrences }: { occurrences: EventOccurrence[] } = await admin("GET", `/events/${event.id}`);
    expect(occurrences).toHaveLength(3);
    const selected = occurrences[0], other = occurrences[1], sibling = occurrences[2];
    const path = `/events/${event.id}/dates/${selected.date}`, otherPath = `/events/${event.id}/dates/${other.date}`;

    // A confirmed guardian receives an opaque copy, not read authority.
    let guardianLogin = "";
    if (teen) {
      guardianLogin = "exact-date-guardian";
      await admin("POST", "/admin/users", { name: "Exact guardian", email: email(guardianLogin), password: PASSWORD, ageBand: "adult" });
      const guardianContext = await browser.newContext({ baseURL: test.info().project.use.baseURL });
      const guardian = await apiAs(guardianContext.request, guardianLogin);
      await guardian("POST", "/me/guardian-claims", { email: email(login) });
      const { guardianLinks } = await admin("GET", "/admin/guardian-links");
      const link = guardianLinks.find((row: { teenUserId?: number }) => row.teenUserId === user.id);
      expect(link).toBeTruthy(); await admin("POST", `/admin/guardian-links/${link.id}/confirm`, { checkNote: "Known parent, checked in person" });
      await guardianContext.close();
    }

    await signIn(page, adminName);
    if (teen) {
      await page.goto("/admin?tab=groups");
      const card = page.locator("div.bg-surface").filter({ has: page.getByRole("link", { name: group.name, exact: true }) });
      await card.getByRole("button", { name: "Manage members", exact: true }).click();
      await card.getByRole("combobox", { name: "Find a person by name" }).fill(name);
      await card.getByRole("option", { name: new RegExp(name) }).click();
      await expect(card.getByRole("group", { name: "Role" })).toHaveCount(0);
      await card.getByRole("button", { name: "Ask to add", exact: true }).click();
      await expect(card.getByRole("button", { name: "Send ask to add" })).toBeVisible();
      await capture(page, "admin-member-manager-teen", [390]);
    }
    await page.goto(`/events/${event.id}`);
    await fromMore(page, "Manage sign-ups", /More for/);
    const picker = page.getByRole("combobox", { name: "Find a person by name" });
    await picker.fill(name); await expect(page.getByRole("option", { name: new RegExp(name) })).toBeVisible();
    await picker.press("ArrowDown"); await picker.press("Enter");
    if (!teen) {
      await page.getByRole("button", { name: "Add", exact: true }).click();
      await expect(page.getByRole("alert")).toContainText("They need to approve this date");
    }
    await page.getByRole("button", { name: "Ask to add", exact: true }).click();
    await page.getByRole("button", { name: "Send ask to add" }).click();
    await page.getByRole("link", { name: "View ask", exact: true }).click();
    const askPath = new URL(page.url()).pathname;
    if (teen) {
      await page.getByRole("button", { name: "Approve adding" }).click();
      await expect(page.getByText("Not added yet", { exact: true })).toBeVisible();
    }
    await signIn(page, login); await page.goto("/notifications");
    await page.getByRole("link", { name: /Review an ask to add/ }).click();
    await expect(page).toHaveURL(new RegExp(askPath + "$"));
    await page.getByRole("button", { name: "Approve adding" }).focus(); await page.keyboard.press("Enter");
    await expect(page.getByText("Added", { exact: true })).toBeVisible();
    await page.goto("/notifications");
    await page.getByRole("link", { name: /You have a date invitation/ }).click();
    await expect(page).toHaveURL(new RegExp(path + "$"));
    await expect(page.getByText("Your answer: Going", { exact: true })).toBeVisible();
    const exact = await (await page.request.get(`/api/v1${path}`)).json();
    for (const key of ["groupId", "groupName", "hostedBy", "hosts", "attendees", "removedRegistrations", "recurrenceRule", "seriesTitle", "description", "canManage", "myHouseholdRegistrations", "myChildcareCount", "myChildcareAgesNote"]) expect(exact.occurrence).not.toHaveProperty(key);
    expect(exact.occurrence.canSeeVideo).toBe(true); expect(exact.occurrence.videoUrl).toBe("https://example.com/exact-private-call");
    save(`${scenario}-safe-date`, exact);
    await expect(page.locator(`a[href="/events/${event.id}"]`)).toHaveCount(0);
    await expect(page.locator(`a[href="/groups/${group.id}"]`)).toHaveCount(0);
    await capture(page, `${scenario}-recipient`);

    // A second explicit accepted date is retained when the selected one ends.
    const search = await admin("GET", `/people/search?q=${encodeURIComponent(name)}`);
    const person = search.people.find((row: { displayName: string }) => row.displayName === name);
    const second = await admin("POST", "/add-requests", { selectionId: person.selectionId, contextType: "event", contextId: event.id, occurrenceId: other.id, childcareCount: 1, childcareAgesNote: "Age 5, keep this care request" });
    if (teen) await admin("POST", `/add-requests/${second.request.id}/decision`, { party: "admin", decision: "approved" });
    await page.goto(`/add-requests/${second.request.id}`); await page.getByRole("button", { name: "Approve adding" }).click();
    await expect(page.getByText("Added", { exact: true })).toBeVisible();
    // The sanitized DTO cannot edit care. A normal own save must omit care
    // rather than clear the accepted request's options with invented defaults.
    await page.goto(otherPath);
    await page.getByRole("button", { name: "Going", exact: true }).click();
    await expect(page.getByText("Your answer: Going", { exact: true })).toBeVisible();
    const retained = await admin("GET", `/events/${event.id}`);
    const retainedCare = retained.occurrences[1].attendees.find((a: { userId: number }) => a.userId === user.id);
    expect(retainedCare.childcareCount).toBe(1); expect(retainedCare.childcareAgesNote).toBe("Age 5, keep this care request");
    save(`${scenario}-care-preserved`, retainedCare);
    await page.goto("/calendar"); await page.getByRole("button", { name: "Just mine", exact: true }).click();
    const selectedLink = page.locator(`main a[href="${path}"]`);
    await expect(selectedLink).toBeVisible(); await expect(page.locator(`main a[href="${otherPath}"]`)).toBeVisible();
    const calendar = await (await page.request.get(`/api/v1/calendar?from=${selected.date}&to=${sibling.date}&tz=UTC&mine=1`)).json();
    save(`${scenario}-calendar`, calendar);
    for (const hidden of [`/events/${event.id}`, `/groups/${group.id}`, `/events/${event.id}/dates/${sibling.date}`]) expect((await page.request.get(`/api/v1${hidden}`)).status()).toBe(404);
    await selectedLink.focus(); await page.keyboard.press("Enter");
    await expect(page.getByText("Your answer: Going", { exact: true })).toBeVisible();
    // Own scoped RSVP, not household/proxy or ordinary RSVP. The real server
    // returns the current answer after each save; mode/guests are explicit.
    if (scenario === "adult-decline") {
      await admin("PUT", `/events/${event.id}`, { capacity: 1 });
      await page.getByLabel("Guests", { exact: true }).fill("2");
      await page.getByRole("button", { name: "Going", exact: true }).click();
      await expect(page.getByRole("alert")).toHaveText(/This event is full/);
      await expect(page.getByLabel("Guests", { exact: true })).toHaveValue("2");
      await capture(page, "capacity-error-preserves-values");
      await admin("PUT", `/events/${event.id}`, { capacity: 10 });
      await page.getByLabel("Guests", { exact: true }).fill("0");
      await page.getByRole("button", { name: "Maybe", exact: true }).click();
      await expect(page.getByText("Your answer: Maybe", { exact: true })).toBeVisible();
      await expect(page.getByRole("link", { name: /Join video call/ })).toHaveCount(0);
      const maybe = await (await page.request.get(`/api/v1${path}`)).json();
      expect(maybe.occurrence.canSeeVideo).toBe(false); expect(maybe.occurrence).not.toHaveProperty("videoUrl");
      save("maybe-video-not-disclosed", maybe); await capture(page, "maybe-no-video");
    }
    await page.getByLabel("Remote", { exact: true }).check(); await page.getByLabel("Guests", { exact: true }).fill("1");
    const saved = page.waitForRequest((r) => r.method() === "PUT" && r.url().endsWith(`${path}/registration`));
    await page.getByRole("button", { name: "Going", exact: true }).focus(); await page.keyboard.press("Enter");
    expect((await saved).postDataJSON()).toEqual({ status: "going", attendanceMode: "remote", guestCount: 1 });
    await expect(page.getByText("Attending: Remote · Guests: 1", { exact: true })).toBeVisible();
    await capture(page, `${scenario}-remote-answer`);
    const grants = await (await page.request.get("/api/v1/contact-grants")).json(); expect(grants.grants).toEqual([]);

    // Roster provenance comes only from the manager response, never source inference.
    const managerContext = await browser.newContext({ baseURL: test.info().project.use.baseURL });
    const managerPage = await managerContext.newPage(); await signIn(managerPage, adminName); await managerPage.goto(`/events/${event.id}`);
    await fromMore(managerPage, "Manage sign-ups", /More for/);
    await expect(managerPage.getByText("Added by a leader", { exact: true })).toBeVisible();
    await capture(managerPage, `${scenario}-manager-provenance`);
    await managerContext.close();

    if (teen) {
      const guardianContext = await browser.newContext({ baseURL: test.info().project.use.baseURL });
      const guardianPage = await guardianContext.newPage(); await signIn(guardianPage, guardianLogin); await guardianPage.goto("/notifications");
      const notices = await (await guardianPage.request.get("/api/v1/notifications")).json();
      const serialized = JSON.stringify(notices);
      expect(serialized).not.toContain(title); expect(serialized).not.toContain(group.name); expect(serialized).not.toContain("Invited date room");
      save("guardian-opaque-notices", notices);
      await guardianPage.getByRole("link", { name: /You have a date invitation/ }).first().click();
      await expect(guardianPage.getByText("This date is not available", { exact: true })).toBeVisible();
      expect((await guardianPage.request.get(`/api/v1${path}`)).status()).toBe(404);
      await capture(guardianPage, "guardian-no-date-authority"); await guardianContext.close();
    }

    if (scenario === "teen-removal") {
      const roster = await admin("GET", `/events/${event.id}`);
      const registrationId = roster.occurrences[0].attendees.find((a: { userId: number }) => a.userId === user.id).registrationId;
      await admin("POST", `/events/${event.id}/registrations/remove`, { date: selected.date, registrationId });
      await page.evaluate(() => window.dispatchEvent(new Event("focus")));
    } else {
      const block = scenario === "adult-block";
      page.once("dialog", async (dialog) => { expect(dialog.message()).toContain("Other accepted dates stay yours"); await dialog.accept(); });
      await page.getByRole("button", { name: block ? "Don't add me again" : "Can't go", exact: true }).click();
    }
    await expect(page.getByText("This date is not available", { exact: true })).toBeVisible();
    await expect(page.getByText(title, { exact: true })).toHaveCount(0); await expect(page.getByRole("link", { name: /Join video call/ })).toHaveCount(0);
    await capture(page, `${scenario}-unavailable`);
    expect((await page.request.get(`/api/v1${path}`)).status()).toBe(404);
    expect((await page.request.get(`/api/v1${otherPath}`)).status()).toBe(200);
    await page.getByRole("link", { name: "Back to calendar", exact: true }).click();
    await page.getByRole("button", { name: "Just mine", exact: true }).click();
    await expect(page.locator(`main a[href="${path}"]`)).toHaveCount(0);
    await expect(page.locator(`main a[href="${otherPath}"]`)).toBeVisible();

    if (scenario === "adult-block") {
      const adminCSRF = (await (await request.get("/api/v1/auth/csrf")).json()).csrfToken;
      const refused = await request.post(`/api/v1/events/${event.id}/registrations/add`, { headers: { "X-CSRF-Token": adminCSRF }, data: { userId: user.id, date: sibling.date } });
      expect(refused.status()).toBe(403); save("block-future-manager-add", { status: refused.status(), body: await refused.json() });
      // This is an explicit new ordinary eligibility grant, not an RSVP fallback:
      // the block does not prevent voluntary sign-up once a person joins.
      await admin("POST", `/admin/groups/${group.id}/members`, { userId: user.id, role: "member" });
      await page.goto(`/events/${event.id}`);
      const csrf = (await (await page.request.get("/api/v1/auth/csrf")).json()).csrfToken;
      const voluntary = await page.request.put(`/api/v1/events/${event.id}/registrations`, { headers: { "X-CSRF-Token": csrf }, data: { date: sibling.date, status: "going", attendanceMode: "in_person", guestCount: 0 } });
      expect(voluntary.status(), await voluntary.text()).toBe(200); save("block-voluntary-ordinary", await voluntary.json());
    }
  });
}
