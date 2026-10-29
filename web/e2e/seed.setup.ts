import AxeBuilder from "@axe-core/playwright";
import { expect, test as setup } from "@playwright/test";
import { apiAs, email, PASSWORD } from "./helpers";

// Runs once per `make e2e`, on the fresh database e2e/server.sh made.
// Members-only circle (private) and Invite-only circle (Sam is a member).
// Room 201 class (limit 3): Grier (admin, leader), Lee (leader), Sam
// (member). Pat and Quinn were disabled while Sam took the last seat, so
// re-enabling them left both out of the group (DEC-47).
setup("first-run setup and seed data", async ({ page, request }) => {
  await page.goto("/setup");
  // The setup page is only reachable now, so its axe scan and screenshots
  // (when SCREENS_DIR is set, as in screens.spec.ts) happen here.
  for (const width of [390, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    const { violations } = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
    expect(violations.filter((v) => v.impact === "moderate" || v.impact === "serious" || v.impact === "critical").map((v) => v.id), `setup at ${width}px`).toEqual([]);
  }
  if (process.env.SCREENS_DIR) {
    for (const w of [390, 768, 1280]) {
      await page.setViewportSize({ width: w, height: 900 });
      await page.screenshot({ path: `${process.env.SCREENS_DIR}/setup-${w}.png`, fullPage: true, animations: "disabled", caret: "hide" });
    }
  }
  await page.locator("[name=site_name]").fill("Fold e2e");
  await page.locator("[name=timezone]").fill("America/Boise");
  await page.locator("[name=name]").fill("Grier");
  await page.locator("[name=email]").fill(email("grier"));
  await page.locator("[name=age_band]").check();
  // A bcrypt failure reaches the same generic recovery UI as an unavailable
  // database, without interrupting Postgres or storing an administrator.
  await page.locator("[name=password]").fill("p".repeat(73));
  await page.locator("[name=password_confirm]").fill("p".repeat(73));
  await page.getByRole("button", { name: "Create site" }).click();
  const alert = page.getByRole("alert");
  await expect(alert).toContainText("We couldn't create your account. Try again.");
  await expect(alert).toBeFocused();
  await expect(page.locator("form")).toHaveAttribute("aria-describedby", "setup-error");
  await expect(page.locator("[name=password]")).toHaveValue("");
  await expect(page.locator("[name=password_confirm]")).toHaveValue("");
  await expect(page.locator("[name=site_name]")).toHaveValue("Fold e2e");
  for (const width of [390, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    const { violations } = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
    expect(violations.filter((v) => v.impact === "moderate" || v.impact === "serious" || v.impact === "critical").map((v) => v.id), `generic setup error at ${width}px`).toEqual([]);
    if (process.env.SCREENS_DIR) await page.screenshot({ path: `${process.env.SCREENS_DIR}/setup-generic-error-${width}.png`, fullPage: true });
  }
  await page.getByRole("link", { name: "Return to the setup form to try again" }).click();
  await expect(page.locator("form")).toBeFocused();
  await page.locator("[name=password]").fill(PASSWORD);
  await page.locator("[name=password_confirm]").fill(PASSWORD);
  await page.getByRole("button", { name: "Create site" }).click();
  await expect(page).not.toHaveURL(/\/setup/);

  const admin = await apiAs(request, "grier");
  const ids: Record<string, number> = {};
  for (const [name, full] of [["lee", "Lee Leader"], ["sam", "Sam Member"], ["pat", "Pat Returner"], ["quinn", "Quinn Returner"]]) {
    const { user } = await admin("POST", "/admin/users", { ageBand: "adult", email: email(name), name: full, password: PASSWORD, role: "member" });
    ids[name] = user.id;
  }
  const { group } = await admin("POST", "/groups", {
    name: "Room 201 class", description: "A class in a small room.", tags: ["class"],
    imageUrl: "", location: "Room 201", visibility: "public", memberLimit: null,
  });
  await admin("POST", `/admin/groups/${group.id}/members`, { userId: ids.lee, role: "leader" });
  for (const name of ["pat", "quinn"]) {
    await admin("POST", `/admin/groups/${group.id}/members`, { userId: ids[name], role: "member" });
    await admin("PUT", `/admin/users/${ids[name]}`, { status: "disabled" });
  }
  await admin("POST", `/admin/groups/${group.id}/members`, { userId: ids.sam, role: "member" });
  await admin("PUT", `/groups/${group.id}`, { memberLimit: 3 });
  for (const name of ["pat", "quinn"]) await admin("PUT", `/admin/users/${ids[name]}`, { status: "active" });

  // Groups only a signed-in member may see (session.spec.ts checks logout
  // leaves none of them behind).
  for (const [name, visibility] of [["Members-only circle", "private"], ["Invite-only circle", "invite_only"]]) {
    const { group: g } = await admin("POST", "/groups", {
      name, description: "Not for visitors.", tags: [], imageUrl: "", location: "", visibility, memberLimit: null,
    });
    await admin("POST", `/admin/groups/${g.id}/members`, { userId: ids.sam, role: "member" });
  }
  // Dedicated household fixtures leave the existing account-only flows intact.
  await admin("POST", "/admin/users", { ageBand: "adult", name: "Household Parent", email: email("household-parent"), password: PASSWORD, role: "member" });
  const parent = await apiAs(request, "household-parent");
  const { person: mary } = await parent("POST", "/me/household/people", { firstName: "Mary", kind: "adult", birthDate: "1990-01-01" });
  const { person: kit } = await parent("POST", "/me/household/people", { firstName: "Kit", kind: "child", birthDate: "2020-05-01", careNotes: "Allergy: follow the care plan.\n" + "LongCareNote".repeat(120) });
  const adminAgain = await apiAs(request, "grier");
  await adminAgain("POST", `/admin/guardian-links/${kit.guardianLinkId}/confirm`, { checkNote: "Known family" });
  const starts = new Date(Date.now() + 6 * 3600_000).toISOString().slice(0, 16);
  const { event: householdEvent } = await adminAgain("POST", `/groups/${group.id}/events`, { title: "Household check night", startsLocal: starts, durationMinutes: 60, tzid: "UTC", capacity: 1, waitlistEnabled: true, remoteAvailable: true, remoteCapacity: 1, childcareAvailable: true, childcareCapacity: 1 });
  const parentAgain = await apiAs(request, "household-parent");
  await parentAgain("PUT", `/events/${householdEvent.id}/registrations`, { date: starts.slice(0, 10), status: "going", attendanceMode: "remote", people: [{ personId: kit.id, status: "going", attendanceMode: "in_person", childcareRequested: true }, { personId: mary.id, status: "going", attendanceMode: "in_person" }] });
  // The household seed's notices are handled; leave the admin bell as the
  // other specs expect.
  await (await apiAs(request, "grier"))("POST", "/notifications/read-all", {});
});
