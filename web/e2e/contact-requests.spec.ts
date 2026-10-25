import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { apiAs, email, fromMore, PASSWORD, signIn } from "./helpers";
// @ts-expect-error plain JS module shared with verify-ui
import { measure } from "./ui-measure.mjs";

test.use({ actionTimeout: 10_000 });

const adminName = process.env.VERIFY_URL ? "verify-admin" : "grier";
const proof = process.env.CONTACT_PROOF_DIR;
const results: unknown[] = [];
async function capture(page: Page, name: string) {
  if (!proof) return;
  mkdirSync(proof, { recursive: true });
  for (const width of [390, 768, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    await page.waitForLoadState("networkidle");
    await page.evaluate(() => window.scrollTo(0, 0));
    const layout = await page.evaluate(measure);
    const { violations } = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
    const axe = violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => v.id);
    await page.screenshot({ path: `${proof}/${name}-${width}.png`, fullPage: true });
    results.push({ name, width, layout, axe });
    writeFileSync(`${proof}/results.json`, JSON.stringify(results, null, 2));
    expect(layout.overflow).toBe(0); expect(layout.cutOff).toEqual([]); expect(layout.clipped).toEqual([]); expect(layout.under24).toEqual([]); expect(axe).toEqual([]);
  }
}
async function save(name: string, data: unknown) {
  if (proof) { mkdirSync(proof, { recursive: true }); writeFileSync(`${proof}/${name}.json`, JSON.stringify(data, null, 2)); }
}

// Dedicated people/context leave the shared e2e seed unchanged. The optional
// proof mode runs this same spec on an isolated verify-fold instance.
test("hidden adult requests preserve failures, disclose only on explicit reveal, and clear on close/logout", async ({ page, request }) => {
  test.setTimeout(120_000);
  const admin = await apiAs(request, adminName);
  const ids: Record<string, number> = {};
  for (const [key, name] of [["requester", "Contact Lee"], ["adult", "Contact Pat"]]) {
    ids[key] = (await admin("POST", "/admin/users", { name, email: email(`contact-${key}`), password: PASSWORD, ageBand: "adult", phone: "2085550142" })).user.id;
  }
  const { group } = await admin("POST", "/groups", { name: "Contact study", description: "Contact request proof", visibility: "public", tags: [] });
  await admin("POST", `/admin/groups/${group.id}/members`, { userId: ids.requester, role: "leader" });
  await admin("POST", `/admin/groups/${group.id}/members`, { userId: ids.adult, role: "member" });
  const adult = await apiAs(request, "contact-adult");
  await adult("PUT", "/me", { hideContactFromLeaders: true });
  await signIn(page, "contact-requester");
  await page.goto(`/groups/${group.id}`);
  await page.getByRole("button", { name: /^members$/i }).click();
  const roster = await (await page.request.get(`/api/v1/groups/${group.id}/members`)).json();
  const hidden = roster.members.find((m: { id: number }) => m.id === ids.adult);
  expect(hidden).not.toHaveProperty("email"); expect(hidden).not.toHaveProperty("phone");
  await save("adult-roster-omissions", roster);
  await page.getByRole("button", { name: "Request direct contact" }).click();
  await page.getByLabel("Email and phone", { exact: true }).check();
  await page.getByLabel(/Reason/).fill("Please coordinate transport with me.");
  // A controlled server failure proves the real form retains valid values.
  await page.route("**/api/v1/contact-requests", (route) => route.request().method() === "POST" ? route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ code: "contact_open_limit", error: "not for display" }) }) : route.continue());
  await page.getByRole("button", { name: "Send request" }).click();
  await expect(page.getByRole("alert")).toContainText("10 open contact requests");
  await expect(page.getByLabel(/Reason/)).toHaveValue("Please coordinate transport with me.");
  await expect(page.getByLabel("Email and phone", { exact: true })).toBeChecked();
  await capture(page, "adult-compose-failed");
  await page.unroute("**/api/v1/contact-requests");
  await page.getByRole("button", { name: "Send request" }).click();
  await page.getByRole("link", { name: "View request", exact: true }).click();
  const path = new URL(page.url()).pathname;
  await expect(page.getByRole("button", { name: "Withdraw request" })).toBeVisible();
  await capture(page, "adult-requester-pending");
  await signIn(page, "contact-adult"); await page.goto(path);
  await expect(page.getByRole("button", { name: "Approve sharing" })).toBeVisible();
  await capture(page, "adult-subject-review");
  await page.getByRole("button", { name: "Approve sharing" }).focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("button", { name: "Revoke access" })).toBeVisible();
  await signIn(page, "contact-requester"); await page.goto(path);
  await expect(page.getByRole("button", { name: "Reveal contact details" })).toBeVisible();
  const detail = await (await page.request.get(`/api/v1${path}`)).json();
  const grants = await (await page.request.get("/api/v1/contact-grants")).json();
  expect(detail.request).not.toHaveProperty("email"); expect(detail.request).not.toHaveProperty("phone");
  for (const grant of grants.grants) { expect(grant).not.toHaveProperty("email"); expect(grant).not.toHaveProperty("phone"); }
  expect(JSON.stringify(detail)).not.toContain(email("contact-adult")); expect(JSON.stringify(grants)).not.toContain(email("contact-adult"));
  await save("adult-review-no-contacts", { detail, grants });
  await expect(page.getByRole("region", { name: "Revealed contact details" })).toHaveCount(0);
  await capture(page, "adult-requester-granted");
  await page.getByRole("button", { name: "Reveal contact details" }).focus(); await page.keyboard.press("Enter");
  await expect(page.getByRole("region", { name: "Revealed contact details" })).toContainText(email("contact-adult"));
  await capture(page, "adult-requester-revealed");
  await page.getByRole("button", { name: "Close contact details" }).click();
  await expect(page.getByRole("region", { name: "Revealed contact details" })).toHaveCount(0);
  await capture(page, "adult-requester-closed");
  await page.goto("/profile");
  await page.getByRole("link", { name: "Direct contact requests and access" }).click();
  await expect(page.getByRole("region", { name: "Request history" })).toBeVisible();
  await capture(page, "adult-requester-history");
  await page.goto(path);
  await page.reload(); await expect(page.getByRole("button", { name: "Reveal contact details" })).toBeVisible();
  await expect(page.getByRole("region", { name: "Revealed contact details" })).toHaveCount(0);
  await page.getByRole("button", { name: "Reveal contact details" }).click();
  await expect(page.getByRole("region", { name: "Revealed contact details" })).toBeVisible();
  await page.getByRole("button", { name: "Log out" }).click();
  await expect(page.getByRole("region", { name: "Revealed contact details" })).toHaveCount(0);
});

test("teen decisions work in both orders; guardian sees only active grants and can revoke; either denial ends review", async ({ page, request }) => {
  test.setTimeout(180_000);
  const admin = await apiAs(request, adminName);
  const { user: guardian } = await admin("POST", "/admin/users", { name: "Contact Guardian", email: email("contact-guardian"), password: PASSWORD, ageBand: "adult" });
  const { user: teen } = await admin("POST", "/admin/users", { name: "Contact Teen", email: email("contact-teen"), password: PASSWORD, ageBand: "teen", birthDate: `${new Date().getUTCFullYear() - 15}-01-01`, phone: "2085550143" });
  await admin("POST", `/admin/users/${teen.id}/teen-consents`, { scope: "account", status: "granted", evidenceText: "Parent signed the account consent form" });
  const guardianApi = await apiAs(request, "contact-guardian");
  await guardianApi("POST", "/me/guardian-claims", { email: teen.email });
  const adminAgain = await apiAs(request, adminName);
  const links = (await adminAgain("GET", "/admin/guardian-links")).guardianLinks;
  const link = links.find((l: { guardianUserId: number; teenUserId: number }) => l.guardianUserId === guardian.id && l.teenUserId === teen.id);
  await adminAgain("POST", `/admin/guardian-links/${link.id}/confirm`, { checkNote: "Known family, checked by church office" });
  const { user: leader } = await adminAgain("POST", "/admin/users", { name: "Contact Teen Leader", email: email("contact-teen-leader"), password: PASSWORD, ageBand: "adult" });
  const startsLocal = new Date(Date.now() + 7 * 86400_000).toISOString().slice(0, 10) + "T18:00";
  const { group } = await adminAgain("POST", "/groups", { name: "Contact youth group", visibility: "invite_only", tags: [] });
  await adminAgain("POST", `/admin/groups/${group.id}/members`, { userId: teen.id, role: "member" });
  await adminAgain("POST", `/admin/groups/${group.id}/members`, { userId: leader.id, role: "leader" });
  const { event: visibleEvent } = await adminAgain("POST", `/groups/${group.id}/events`, { title: "Contact teen date", startsLocal, durationMinutes: 60, tzid: "UTC" });
  const teenApi = await apiAs(request, "contact-teen");
  await teenApi("PUT", `/events/${visibleEvent.id}/registrations`, { date: startsLocal.slice(0, 10), status: "going" });
  for (const first of ["admin", "subject"] as const) {
    await signIn(page, "contact-teen-leader"); await page.goto(`/events/${visibleEvent.id}`);
    await fromMore(page, "Manage sign-ups", /More for/);
    const roster = await (await page.request.get(`/api/v1/events/${visibleEvent.id}`)).json();
    const attendee = roster.occurrences[0].attendees.find((a: { userId: number }) => a.userId === teen.id);
    expect(attendee).not.toHaveProperty("email"); expect(attendee).not.toHaveProperty("phone");
    await save(`teen-${first}-roster-omissions`, roster);
    await page.getByRole("button", { name: "Request direct contact" }).click();
    await page.getByLabel("Email and phone", { exact: true }).check();
    await page.getByLabel(/Reason/).fill("Private teen request reason");
    if (first === "admin") await capture(page, "teen-event-compose");
    await page.getByRole("button", { name: "Send request" }).click();
    await page.getByRole("link", { name: "View request", exact: true }).click();
    const path = new URL(page.url()).pathname;
    const contact = { id: Number(path.split("/").pop()) };
    await signIn(page, "contact-guardian"); await page.goto(path);
    await expect(page.getByText("Contact access is not available", { exact: true })).toBeVisible();
    await expect(page.getByText("Private teen request reason")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Approve sharing" })).toHaveCount(0);
    await signIn(page, first === "admin" ? adminName : "contact-teen"); await page.goto(path);
    await capture(page, `teen-${first}-first-review`);
    const firstSection = page.getByRole("region", { name: first === "admin" ? "Admin's decision" : "Teen's decision", exact: true });
    await firstSection.getByRole("button", { name: "Approve sharing" }).click();
    await expect(page.getByRole("heading", { name: first === "admin" ? "Admin's decision: Approved" : "Teen's decision: Approved", exact: true })).toBeVisible();
    await capture(page, `teen-${first}-first-approved`);
    await signIn(page, first === "admin" ? "contact-teen" : adminName); await page.goto(path);
    await capture(page, `teen-${first}-second-review`);
    await page.getByRole("button", { name: "Approve sharing" }).click();
    await expect(page.getByRole("button", { name: "Revoke access" })).toBeVisible();
    await signIn(page, "contact-guardian"); await page.goto(path);
    await expect(page.getByRole("button", { name: "Revoke access" })).toBeVisible();
    expect((await page.request.get(`/api/v1/contact-requests/${contact.id}`)).status()).toBe(404);
    const active = await (await page.request.get("/api/v1/contact-grants")).json();
    const fact = active.grants.find((g: { requestId: number }) => g.requestId === contact.id);
    expect(fact).not.toHaveProperty("contextId"); expect(fact).not.toHaveProperty("reason"); expect(fact).not.toHaveProperty("subjectDecision"); expect(fact).not.toHaveProperty("email");
    await save(`guardian-${first}-grant-omissions`, active);
    await expect(page.getByRole("button", { name: "Approve sharing" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Reveal contact details" })).toHaveCount(0);
    await expect(page.getByRole("link", { name: "View event" })).toHaveCount(0);
    await capture(page, `guardian-${first}-active-grant`);
    if (first === "admin") {
      await page.goto("/profile"); await page.getByRole("button", { name: "View active contact access" }).click();
      await expect(page.getByRole("region", { name: "Active teen contact grants" }).getByRole("button", { name: "Revoke access" })).toBeVisible();
      await capture(page, "guardian-profile-active-grant");
      await page.goto(path);
    }
    page.once("dialog", (dialog) => dialog.accept());
    await page.getByRole("button", { name: "Revoke access" }).click();
    await expect(page.getByText("Contact access is not available", { exact: true })).toBeVisible();
    await save(`guardian-${first}-ended-omissions`, await (await page.request.get("/api/v1/contact-grants")).json());
  }
  const adminFinal = await apiAs(request, adminName);
  const { request: denied } = await adminFinal("POST", "/contact-requests", { subjectUserId: teen.id, contextType: "event", contextId: visibleEvent.id, fields: "phone" });
  await signIn(page, "contact-teen"); await page.goto(`/contact-requests/${denied.id}`);
  await page.getByRole("button", { name: "Deny request" }).click();
  await expect(page.getByRole("heading", { name: "Teen's decision: Denied", exact: true })).toBeVisible();
  await signIn(page, adminName); await page.goto(`/contact-requests/${denied.id}`);
  await expect(page.getByRole("button", { name: "Approve sharing" })).toHaveCount(0);
  await capture(page, "teen-denied-terminal");
});
