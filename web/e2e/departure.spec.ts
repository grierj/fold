import AxeBuilder from "@axe-core/playwright";
import { mkdirSync } from "node:fs";
import { expect, test } from "@playwright/test";
import { apiAs, email, fromMore, PASSWORD, signInTo, soon } from "./helpers";

const proof = process.env.SCREENS_DIR;
const local = (days: number, minutes = 0) => new Date(Date.now() + days * 86400_000 + minutes * 60_000).toISOString().slice(0, 16);

async function screenshot(page: import("@playwright/test").Page, name: string) {
  if (!proof) return;
  mkdirSync(proof, { recursive: true });
  await page.screenshot({ path: `${proof}/${name}.png`, fullPage: true, animations: "disabled", caret: "hide" });
}

async function assertDialogQuality(page: import("@playwright/test").Page) {
  const dialog = page.getByRole("dialog");
  const { violations } = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"]).analyze();
  expect(violations.filter((item) => item.impact === "serious" || item.impact === "critical").map((item) => item.id)).toEqual([]);
  if ((await page.viewportSize())?.width === 390) {
    const sizes = await dialog.locator("button, label.choice-row").evaluateAll((nodes) => nodes.map((node) => {
      const box = node.getBoundingClientRect();
      return { text: node.textContent?.trim(), width: box.width, height: box.height };
    }));
    expect(sizes.filter((size) => size.width < 44 || size.height < 44)).toEqual([]);
  }
}

test("a delayed pre-leave group response cannot restore leader controls or roster data", async ({ page, request }) => {
  test.setTimeout(120_000);
  const suffix = Date.now().toString(36);
  const departingLogin = `departure-refresh-${suffix}`;
  const continuingLogin = `departure-continuing-${suffix}`;
  const asAdmin = async (method: string, path: string, data?: unknown) => (await apiAs(request, "grier"))(method, path, data);
  const departing = (await asAdmin("POST", "/admin/users", { ageBand: "adult", name: "Departing Leader", email: email(departingLogin), password: PASSWORD, role: "member" })).user as { id: number };
  const continuing = (await asAdmin("POST", "/admin/users", { ageBand: "adult", name: "Continuing Leader", email: email(continuingLogin), password: PASSWORD, role: "member" })).user as { id: number };
  const { group } = await asAdmin("POST", "/groups", { name: `Departure refresh ${suffix}`, description: "Refresh ordering proof", tags: [], location: "Hall", visibility: "public", memberLimit: null }) as { group: { id: number } };
  await asAdmin("POST", `/admin/groups/${group.id}/members`, { userId: departing.id, role: "leader" });
  await asAdmin("POST", `/admin/groups/${group.id}/members`, { userId: continuing.id, role: "leader" });
  const startsLocal = local(14);
  await asAdmin("POST", `/groups/${group.id}/events`, { title: "Departure refresh date", startsLocal, durationMinutes: 60, tzid: "UTC", visibility: "public", capacity: null, waitlistEnabled: true });

  await page.setViewportSize({ width: 390, height: 900 });
  await signInTo(page, departingLogin, `/groups/${group.id}`);
  await expect(page.getByRole("heading", { name: `Departure refresh ${suffix}` })).toBeVisible();

  let releaseOld!: () => void;
  let markOldCaptured!: () => void;
  let markOldFulfilled!: () => void;
  const oldCaptured = new Promise<void>((resolve) => { markOldCaptured = resolve; });
  const oldRelease = new Promise<void>((resolve) => { releaseOld = resolve; });
  const oldFulfilled = new Promise<void>((resolve) => { markOldFulfilled = resolve; });
  let held = false;
  const groupRoute = `**/api/v1/groups/${group.id}`;
  await page.route(groupRoute, async (route) => {
    if (route.request().method() !== "GET" || held) return route.continue();
    held = true;
    const response = await route.fetch();
    markOldCaptured();
    await oldRelease;
    await route.fulfill({ response });
    markOldFulfilled();
  });

  await page.getByRole("group", { name: "Your answer" }).getByRole("button", { name: "Maybe" }).click();
  await oldCaptured;
  await page.getByRole("button", { name: "More group actions" }).click();
  await page.getByRole("menuitem", { name: "Leave group" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Leave group" }).click();
  await expect(page.getByRole("button", { name: "Join this group" })).toBeVisible();
  await expect(page.getByText("0 dates kept · 1 date ended.")).toBeVisible();
  await page.getByRole("tab", { name: "Members", exact: true }).click();
  await expect(page.getByText("Continuing Leader", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "Schedule event" })).toHaveCount(0);

  releaseOld();
  await oldFulfilled;
  await expect(page.getByRole("button", { name: "Join this group" })).toBeVisible();
  await expect(page.getByText("Continuing Leader", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "Schedule event" })).toHaveCount(0);
  const { violations } = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"]).analyze();
  expect(violations.filter((item) => item.impact === "serious" || item.impact === "critical").map((item) => item.id)).toEqual([]);
  await screenshot(page, "departure-refresh-boundary-390");
  await page.unroute(groupRoute);

  // A voluntary leaver rejoins themselves, without a manager restoring them.
  await page.getByRole("button", { name: "Join this group" }).click();
  await expect(page.getByRole("button", { name: "Leave group", exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Schedule event" })).toHaveCount(0);
  await page.reload();
  await expect(page.getByRole("button", { name: "Leave group", exact: true })).toBeVisible();
  await screenshot(page, "departure-self-rejoined-390");
});

test("departure chooser handles self, leader and admin paths, kept private access, zero dates, cancel and stale recovery", async ({ page, request }) => {
  test.setTimeout(120_000);
  const suffix = Date.now().toString(36);
  const memberLogin = `departure-member-${suffix}`;
  const leaderLogin = `departure-leader-${suffix}`;
  const removedLogin = `departure-removed-${suffix}`;
  const adminRemovedLogin = `departure-admin-removed-${suffix}`;
  const staleLogin = `departure-stale-${suffix}`;
  const zeroLogin = `departure-zero-${suffix}`;
  const asAdmin = async (method: string, path: string, data?: unknown) => (await apiAs(request, "grier"))(method, path, data);
  const makeUser = async (login: string, name: string) => (await asAdmin("POST", "/admin/users", { ageBand: "adult", name, email: email(login), password: PASSWORD, role: "member" })).user as { id: number };
  const member = await makeUser(memberLogin, "Departure Member");
  const leader = await makeUser(leaderLogin, "Departure Leader");
  const removed = await makeUser(removedLogin, "Leader Removal Target");
  const adminRemoved = await makeUser(adminRemovedLogin, "Admin Removal Target");
  const stale = await makeUser(staleLogin, "Stale Preview Member");
  const zero = await makeUser(zeroLogin, "Zero Date Member");

  const makeGroup = async (name: string, visibility: "public" | "private" | "invite_only" = "invite_only") => (await asAdmin("POST", "/groups", { name, description: "Departure browser proof", tags: [], location: "", visibility, memberLimit: null })).group as { id: number };
  const selfGroup = await makeGroup(`Departure private ${suffix}`);
  for (const [id, role] of [[leader.id, "leader"], [member.id, "member"]] as const) await asAdmin("POST", `/admin/groups/${selfGroup.id}/members`, { userId: id, role });

  const createEvent = async (groupId: number, title: string, startsLocal: string, visibility: "public" | "private" | "invite_only" = "invite_only", durationMinutes = 60) =>
    (await asAdmin("POST", `/groups/${groupId}/events`, { title, startsLocal, durationMinutes, tzid: "UTC", visibility, capacity: null, waitlistEnabled: true })).event as { id: number };
  const underwayAt = local(0, -10);
  const underway = await createEvent(selfGroup.id, "Kept underway private date", underwayAt, "invite_only", 120);
  const publicAt = local(5);
  const publicEvent = await createEvent(selfGroup.id, "Ended public date", publicAt, "public");
  const privateAt = local(6);
  const privateEvent = await createEvent(selfGroup.id, "Ended private date", privateAt, "invite_only");
  const proxyAt = local(7);
  const proxyEvent = await createEvent(selfGroup.id, "Proxy attendance date", proxyAt, "invite_only");
  const pastAt = local(-3);
  const pastEvent = await createEvent(selfGroup.id, "Past attendance date", pastAt, "invite_only");
  const memberApi = await apiAs(request, memberLogin);
  for (const [event, at, status] of [[underway, underwayAt, "going"], [publicEvent, publicAt, "going"], [privateEvent, privateAt, "maybe"], [pastEvent, pastAt, "going"]] as const) {
    await memberApi("PUT", `/events/${event.id}/registrations`, { date: at.slice(0, 10), status, attendanceMode: "in_person", people: [] });
  }

  // A proxy registration belongs to the household person and must survive the account holder's departure.
  const { person: child } = await memberApi("POST", "/me/household/people", { firstName: "Proxy Child", kind: "child", birthDate: "2020-01-01" });
  await asAdmin("POST", `/admin/guardian-links/${child.guardianLinkId}/confirm`, { checkNote: "Browser departure proof" });
  await (await apiAs(request, memberLogin))("PUT", `/events/${proxyEvent.id}/registrations`, { date: proxyAt.slice(0, 10), status: "declined", attendanceMode: "in_person", people: [{ personId: child.id, status: "going", attendanceMode: "in_person", childcareRequested: false }] });

  await signInTo(page, memberLogin, `/groups/${selfGroup.id}`);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "Leave group", exact: true }).click();
  await expect(page.getByRole("dialog", { name: new RegExp(`Leave Departure private ${suffix}`) })).toBeVisible();
  await expect(page.getByText(/3 upcoming dates, including 2 dates you said Going to/)).toBeVisible();
  await expect(page.getByRole("checkbox", { name: /Keep Kept underway private date/ })).toBeChecked();
  await expect(page.getByRole("checkbox", { name: /Keep Ended public date/ })).not.toBeChecked();
  await expect(page.getByText("Your past attendance stays.")).toBeVisible();
  await assertDialogQuality(page);
  await screenshot(page, "departure-self-dialog-390");
  for (const width of [768, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    await assertDialogQuality(page);
    await screenshot(page, `departure-self-dialog-${width}`);
  }
  await page.setViewportSize({ width: 390, height: 844 });

  let releaseDeparture!: () => void;
  let markDeparturePending!: () => void;
  let markDepartureContinued!: () => void;
  const departurePending = new Promise<void>((resolve) => { markDeparturePending = resolve; });
  const departureRelease = new Promise<void>((resolve) => { releaseDeparture = resolve; });
  const departureContinued = new Promise<void>((resolve) => { markDepartureContinued = resolve; });
  const leaveRoute = `**/api/v1/groups/${selfGroup.id}/leave`;
  await page.route(leaveRoute, async (route) => {
    markDeparturePending();
    await departureRelease;
    await route.continue();
    markDepartureContinued();
  });
  await page.getByRole("button", { name: "Leave group", exact: true }).last().click();
  await departurePending;
  const pendingDialog = page.getByRole("dialog");
  await expect(pendingDialog).toHaveAttribute("aria-busy", "true");
  await expect(pendingDialog).toBeFocused();
  await expect(pendingDialog.locator("button:enabled, input:enabled, textarea:enabled")).toHaveCount(0);
  await page.keyboard.press("Tab");
  expect(await pendingDialog.evaluate((node) => node.contains(document.activeElement))).toBe(true);
  await page.keyboard.press("Shift+Tab");
  expect(await pendingDialog.evaluate((node) => node.contains(document.activeElement))).toBe(true);
  await screenshot(page, "departure-self-pending-390");
  releaseDeparture();
  await departureContinued;
  await page.unroute(leaveRoute);
  await expect(page).toHaveURL(/\/my-groups$/);
  await expect(page.getByText("1 date kept · 2 dates ended.")).toBeVisible();

  const keptDate = underwayAt.slice(0, 10);
  await page.goto(`/events/${underway.id}/dates/${keptDate}`);
  await expect(await soon(page.getByRole("heading", { name: "Kept underway private date", level: 1 }))).toBeVisible();
  await expect(page.getByRole("button", { name: "Going", exact: true })).toHaveAttribute("aria-pressed", "true");
  await page.goto(`/events/${underway.id}`);
  await expect(page.getByRole("heading", { name: "Event not found" })).toBeVisible();
  await page.goto(`/groups/${selfGroup.id}`);
  await expect(page.getByRole("heading", { name: "Group not found" })).toBeVisible();
  await page.goto(`/events/${privateEvent.id}/dates/${privateAt.slice(0, 10)}`);
  await expect(await soon(page.getByText("This date is not available"))).toBeVisible();

  await signInTo(page, leaderLogin, `/events/${proxyEvent.id}`);
  await page.getByRole("button", { name: "Show who's coming" }).click();
  await expect(page.getByText("Proxy Child")).toBeVisible();
  await page.goto(`/groups/${selfGroup.id}`);
  await page.getByRole("tab", { name: "Members", exact: true }).click();
  await expect(page.getByText("Departure Member")).toHaveCount(0);

  // Leader removal keeps the note in the dialog and uses the same canonical confirmation.
  const leaderGroup = await makeGroup(`Leader removal ${suffix}`, "public");
  await asAdmin("POST", `/admin/groups/${leaderGroup.id}/members`, { userId: leader.id, role: "leader" });
  await asAdmin("POST", `/admin/groups/${leaderGroup.id}/members`, { userId: removed.id, role: "member" });
  const removeAt = local(8);
  const removeEvent = await createEvent(leaderGroup.id, "Leader removal public date", removeAt, "public");
  await (await apiAs(request, removedLogin))("PUT", `/events/${removeEvent.id}/registrations`, { date: removeAt.slice(0, 10), status: "going", attendanceMode: "in_person", people: [] });
  await page.goto(`/groups/${leaderGroup.id}`);
  await page.getByRole("tab", { name: "Members", exact: true }).click();
  await fromMore(page.getByText("Leader Removal Target", { exact: true }).locator("../.."), "Remove from group");
  const leaderDialog = page.getByRole("dialog", { name: "Remove Leader Removal Target?" });
  await expect(leaderDialog).toBeVisible();
  await leaderDialog.getByLabel("Note for church admins (optional)").fill("Browser note preserved");
  await screenshot(page, "departure-leader-remove-390");
  await leaderDialog.getByRole("button", { name: "Remove from group" }).click();
  await expect(page.getByText("0 dates kept · 1 date ended.")).toBeVisible();
  await expect(page.getByText("Leader Removal Target")).toHaveCount(0);

  // Admin removal reaches the same chooser from the admin roster.
  const adminGroup = await makeGroup(`Admin removal ${suffix}`, "public");
  await asAdmin("POST", `/admin/groups/${adminGroup.id}/members`, { userId: adminRemoved.id, role: "member" });
  await signInTo(page, "grier", "/admin?tab=groups");
  const adminCard = page.getByRole("link", { name: `Admin removal ${suffix}` }).locator("../../..");
  const manageMembers = adminCard.getByRole("button", { name: "Manage members" });
  await expect(manageMembers).toBeVisible({ timeout: 10_000 });
  await manageMembers.click();
  const adminTargetRow = adminCard.getByText("Admin Removal Target", { exact: true }).locator("../..");
  const adminRemove = adminTargetRow.getByRole("button", { name: "Remove" });
  await expect(adminRemove).toBeVisible({ timeout: 10_000 });
  await adminRemove.click();
  const adminDialog = page.getByRole("dialog", { name: "Remove Admin Removal Target?" });
  await expect(adminDialog).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  await assertDialogQuality(page);
  await screenshot(page, "departure-admin-remove-390");
  for (const width of [768, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    await assertDialogQuality(page);
    await screenshot(page, `departure-admin-remove-${width}`);
  }
  await page.setViewportSize({ width: 390, height: 844 });

  const adminDepartureRoute = `**/api/v1/groups/${adminGroup.id}/members/${adminRemoved.id}/departure`;
  const adminPreviewRoute = `**/api/v1/groups/${adminGroup.id}/members/${adminRemoved.id}/departure-preview`;
  const adminMembersRoute = `**/api/v1/groups/${adminGroup.id}/members`;
  await page.route(adminDepartureRoute, (route) => route.fulfill({ status: 403, contentType: "application/json", body: JSON.stringify({ error: "private", code: "departure_unavailable" }) }));
  await page.route(adminPreviewRoute, (route) => route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ error: "private", code: "invalid" }) }));
  await page.route(adminMembersRoute, (route) => route.fulfill({ status: 403, contentType: "application/json", body: JSON.stringify({ error: "private", code: "invalid" }) }));
  await adminDialog.getByRole("button", { name: "Remove from group" }).click();
  await expect(page.getByRole("dialog", { name: "Departure unavailable" })).toBeVisible();
  await expect(page.getByText("Admin Removal Target", { exact: true })).toHaveCount(0);
  await assertDialogQuality(page);
  await screenshot(page, "departure-admin-unavailable-focus-390");
  await page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(adminCard.getByRole("button", { name: "Manage members" })).toBeFocused();
  await expect(page.getByText("Admin Removal Target", { exact: true })).toHaveCount(0);
  await page.unroute(adminDepartureRoute);
  await page.unroute(adminPreviewRoute);
  await page.unroute(adminMembersRoute);

  await adminCard.getByRole("button", { name: "Manage members" }).click();
  await expect(adminCard.getByText("Admin Removal Target", { exact: true })).toBeVisible();
  await adminCard.getByText("Admin Removal Target", { exact: true }).locator("../..").getByRole("button", { name: "Remove" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Remove from group" }).click();
  await expect(adminCard.getByText("0 dates kept · 0 dates ended.")).toBeVisible();

  // Zero-date departure is still explicit and cancel writes nothing.
  const zeroGroup = await makeGroup(`Zero departure ${suffix}`, "public");
  await asAdmin("POST", `/admin/groups/${zeroGroup.id}/members`, { userId: zero.id, role: "member" });
  await signInTo(page, zeroLogin, `/groups/${zeroGroup.id}`);
  await page.getByRole("button", { name: "Leave group", exact: true }).click();
  await expect(page.getByText("Your past attendance stays.")).toBeVisible();
  await expect(page.getByText(/0 upcoming dates?/i)).toHaveCount(0);
  await assertDialogQuality(page);
  await screenshot(page, "departure-zero-dialog-390");
  for (const width of [768, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    await assertDialogQuality(page);
    await screenshot(page, `departure-zero-dialog-${width}`);
  }
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "Stay" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Leave group", exact: true })).toBeVisible();

  // A new registration makes the zero-date snapshot stale. The UI refreshes defaults and requires a second confirmation.
  const staleGroup = await makeGroup(`Stale departure ${suffix}`, "public");
  await asAdmin("POST", `/admin/groups/${staleGroup.id}/members`, { userId: stale.id, role: "member" });
  const staleAt = local(9);
  const staleEvent = await createEvent(staleGroup.id, "New stale public date", staleAt, "public");
  const staleApi = await apiAs(request, staleLogin);
  await signInTo(page, staleLogin, `/groups/${staleGroup.id}`);
  await page.getByRole("button", { name: "Leave group", exact: true }).click();
  await expect(page.getByText("Your past attendance stays.")).toBeVisible();
  await staleApi("PUT", `/events/${staleEvent.id}/registrations`, { date: staleAt.slice(0, 10), status: "going", attendanceMode: "in_person", people: [] });
  await page.getByRole("dialog").getByRole("button", { name: "Leave group" }).click();
  await expect(page.getByRole("alert")).toHaveText(/refreshed the choices and restored the safest defaults/);
  await expect(page.getByRole("checkbox", { name: /Keep New stale public date/ })).not.toBeChecked();
  await expect(page.getByRole("button", { name: "Leave group", exact: true }).first()).toBeDisabled();
  await screenshot(page, "departure-stale-recovered-390");
  await page.getByRole("dialog").getByRole("button", { name: "Leave group" }).click();
  await expect(page.getByText("0 dates kept · 1 date ended.")).toBeVisible();
});
