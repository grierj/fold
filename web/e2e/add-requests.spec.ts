import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { apiAs, email, fromMore, PASSWORD, signInTo, soon } from "./helpers";
// @ts-expect-error plain JS module shared with verify-ui
import { measure } from "./ui-measure.mjs";

const adminName = process.env.VERIFY_URL ? "verify-admin" : "grier";
const proof = process.env.ADD_PROOF_DIR;
const results: unknown[] = [];
async function capture(page: Page, name: string) {
  if (!proof) return;
  mkdirSync(proof, { recursive: true });
  for (const width of [390, 768, 1280]) {
    await page.setViewportSize({ width, height: 900 }); await page.waitForLoadState("networkidle");
    await page.evaluate(() => window.scrollTo(0, 0));
    const layout = await page.evaluate(measure);
    const { violations } = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
    const axe = violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => v.id);
    await page.screenshot({ path: `${proof}/${name}-${width}.png`, fullPage: true });
    results.push({ name, width, layout, axe }); writeFileSync(`${proof}/results.json`, JSON.stringify(results, null, 2));
    expect(layout.overflow).toBe(0); expect(layout.cutOff).toEqual([]); expect(layout.clipped).toEqual([]); expect(layout.under24).toEqual([]); expect(axe).toEqual([]);
  }
}

test("group teen ask uses opaque selection, both approvals and neutral hidden preview without contact access", async ({ page, request }) => {
  test.setTimeout(180_000);
  const admin = await apiAs(request, adminName);
  const { user: teen } = await admin("POST", "/admin/users", { name: "Ask Teen", email: email("ask-teen"), password: PASSWORD, ageBand: "teen", birthDate: `${new Date().getUTCFullYear() - 15}-01-01` });
  await admin("POST", `/admin/users/${teen.id}/teen-consents`, { scope: "account", status: "granted", evidenceText: "Parent signed the account consent form" });
  const { group } = await admin("POST", "/groups", { name: "Private ask group", visibility: "invite_only", tags: [] });
  await signInTo(page, adminName, `/groups/${group.id}`);
  const membersTab = page.getByRole("tablist", { name: "Group sections", exact: true })
    .getByRole("tab", { name: "Members", exact: true });
  await membersTab.click();
  await expect(membersTab).toHaveAttribute("aria-selected", "true");
  const membersPanel = page.getByRole("tabpanel", { name: "Members", exact: true });
  await expect(membersPanel).toBeVisible();
  await page.getByRole("button", { name: "Add people" }).click();
  const picker = page.getByRole("combobox", { name: "Find a person by name" });
  await picker.fill("Ask Teen"); await expect(await soon(page.getByRole("option", { name: /Ask Teen/ }))).toBeVisible();
  await picker.press("ArrowDown"); await picker.press("Enter");
  await page.getByRole("button", { name: "Ask to add", exact: true }).click();
  await page.getByRole("textbox", { name: /Reason/ }).fill("Join us next week");
  await capture(page, "group-ask-compose");
  await page.getByRole("button", { name: "Send ask to add" }).click();
  await expect(page.getByText(/They have not been added yet/)).toBeVisible();
  await page.getByRole("link", { name: "View ask", exact: true }).click();
  const path = new URL(page.url()).pathname;
  await expect(await soon(page.getByRole("heading", { name: "Admin's decision: Waiting", exact: true }))).toBeVisible();
  await page.getByRole("button", { name: "Approve adding" }).focus(); await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { name: "Admin's decision: Approved", exact: true })).toBeVisible();
  await expect(page.getByText("Not added yet", { exact: true })).toBeVisible();
  await capture(page, "admin-approved-teen-pending");
  await signInTo(page, "ask-teen", "/notifications");
  await page.getByRole("link", { name: /Review an ask to add/ }).click();
  await expect(page).toHaveURL(new RegExp(path + "$"));
  await expect(await soon(page.getByText("A group membership", { exact: true }))).toBeVisible();
  await expect(page.getByText("Private ask group", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "View group" })).toHaveCount(0);
  const detail = await (await page.request.get(`/api/v1${path}`)).json();
  for (const key of ["contextId", "groupName", "requesterName", "subjectName", "email", "phone"]) expect(detail.request).not.toHaveProperty(key);
  expect((await page.request.get(`/api/v1/groups/${group.id}`)).status()).toBe(404);
  if (proof) writeFileSync(`${proof}/hidden-preview.json`, JSON.stringify(detail, null, 2));
  await capture(page, "teen-hidden-group-review");
  await page.getByRole("button", { name: "Approve adding" }).click();
  await expect(page.getByText("Added", { exact: true })).toBeVisible();
  expect((await page.request.get(`/api/v1/groups/${group.id}`)).status()).toBe(200);
  const grants = await (await page.request.get("/api/v1/contact-grants")).json(); expect(grants.grants).toEqual([]);
  await capture(page, "teen-added");
  await page.goto("/profile/requests");
  await (await soon(page.getByRole("link", { name: "Add request history" }))).click();
  await expect(await soon(page.getByRole("link", { name: /Review ask #/ }))).toBeVisible();
  await capture(page, "add-history");
});

test("hidden date adult add offers an explicit ask and a title/time-only review without series access", async ({ page, request }) => {
  test.setTimeout(180_000);
  const admin = await apiAs(request, adminName);
  const { user: adult } = await admin("POST", "/admin/users", { name: "Ask Date Adult", email: email("ask-date-adult"), password: PASSWORD, ageBand: "adult" });
  const { group } = await admin("POST", "/groups", { name: "Hidden date organizer", visibility: "invite_only", tags: [] });
  const date = new Date(Date.now() + 7 * 86400_000).toISOString().slice(0, 10);
  const { event } = await admin("POST", `/groups/${group.id}/events`, { title: "Requested date title", startsLocal: `${date}T18:00`, durationMinutes: 60, tzid: "UTC", recurrenceRule: "FREQ=WEEKLY;COUNT=3" });
  await signInTo(page, adminName, `/events/${event.id}`);
  await fromMore(page, "Manage sign-ups", /More for/);
  const picker = page.getByRole("combobox", { name: "Find a person by name" });
  await (await soon(picker)).fill(adult.name); await (await soon(page.getByRole("option", { name: adult.name, exact: true }))).click();
  await page.getByRole("button", { name: "Add", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("They need to approve this date");
  await expect(page.getByRole("button", { name: "Send ask to add" })).toHaveCount(0);
  await page.getByRole("button", { name: "Ask to add", exact: true }).click();
  await capture(page, "date-ask-compose");
  await page.getByRole("button", { name: "Send ask to add" }).click();
  await page.getByRole("link", { name: "View ask", exact: true }).click();
  const path = new URL(page.url()).pathname;
  await signInTo(page, "ask-date-adult", path);
  await expect(page.getByRole("button", { name: "Approve adding" })).toBeVisible();
  await expect(page.getByText("Requested date title", { exact: true })).toBeVisible();
  const detail = await (await page.request.get(`/api/v1${path}`)).json();
  for (const key of ["contextId", "groupName", "requesterName", "subjectName", "location", "hosts", "email", "phone", "occurrenceId"]) expect(detail.request).not.toHaveProperty(key);
  expect(detail.request.reason).toBe("");
  expect((await page.request.get(`/api/v1/events/${event.id}`)).status()).toBe(404);
  expect((await page.request.get(`/api/v1/groups/${group.id}`)).status()).toBe(404);
  await expect(page.getByRole("link", { name: /View event|View group/ })).toHaveCount(0);
  if (proof) writeFileSync(`${proof}/date-preview.json`, JSON.stringify(detail, null, 2));
  await capture(page, "date-title-time-review");
  await page.getByRole("button", { name: "Approve adding" }).click();
  await expect(page.getByText("Added", { exact: true })).toBeVisible();
  expect((await page.request.get(`/api/v1/events/${event.id}`)).status()).toBe(404);
  expect((await page.request.get(`/api/v1/events/${event.id}/dates/${date}`)).status()).toBe(200);
  // Recipient SPA/date controls are a separate implementation partition.
});
