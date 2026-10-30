import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { apiAs, email, fromMore, PASSWORD, signIn, signInTo, soon } from "./helpers";
import type { ContactHistoryResponse } from "../src/types";
// @ts-expect-error plain JS module shared with verify-ui
import { measure } from "./ui-measure.mjs";

test.use({ actionTimeout: 10_000 });
const adminName = process.env.VERIFY_URL ? "verify-admin" : "grier";
const proof = process.env.HISTORY_PROOF_DIR;
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
function save(name: string, value: unknown) {
  if (proof) { mkdirSync(proof, { recursive: true }); writeFileSync(`${proof}/${name}.json`, JSON.stringify(value, null, 2)); }
}

test("admin user entry reviews retained history, filters and CSV; nonadmins get no history or export", async ({ page, request }) => {
  test.setTimeout(180_000);
  page.on("dialog", (dialog) => dialog.accept());
  const suffix = Date.now().toString();
  const leaderLogin = `history-s5-leader-${suffix}`;
  const adultLogin = `history-s5-adult-${suffix}`;
  const admin = await apiAs(request, adminName);
  const { user: leader } = await admin("POST", "/admin/users", { name: `History Leader ${suffix}`, email: email(leaderLogin), password: PASSWORD, ageBand: "adult" });
  const { user: adult } = await admin("POST", "/admin/users", { name: "History Adult", email: email(adultLogin), phone: "2085550197", password: PASSWORD, ageBand: "adult" });
  const { user: added } = await admin("POST", "/admin/users", { name: "History Added", email: email(`history-s5-added-${suffix}`), password: PASSWORD, ageBand: "adult" });
  const { group } = await admin("POST", "/groups", { name: "History S5 study", description: "History evidence", visibility: "public", tags: [] });
  await admin("POST", `/admin/groups/${group.id}/members`, { userId: leader.id, role: "leader" });
  await admin("POST", `/admin/groups/${group.id}/members`, { userId: adult.id, role: "member" });
  await (await apiAs(request, adultLogin))("PUT", "/me", { hideContactFromLeaders: true });
  await signInTo(page, leaderLogin, `/groups/${group.id}`);
  const addCSRF = (await (await page.request.get("/api/v1/auth/csrf")).json()).csrfToken;
  const addition = await page.request.post(`/api/v1/groups/${group.id}/members`, { headers: { "X-CSRF-Token": addCSRF }, data: { email: added.email } });
  expect(addition.ok()).toBe(true);
  await page.reload();
  const membersTab = page.getByRole("tablist", { name: "Group sections", exact: true })
    .getByRole("tab", { name: "Members", exact: true });
  await membersTab.click();
  await expect(membersTab).toHaveAttribute("aria-selected", "true");
  const membersPanel = page.getByRole("tabpanel", { name: "Members", exact: true });
  await expect(membersPanel).toBeVisible();
  await page.getByRole("button", { name: "Request direct contact" }).click();
  await (await soon(page.getByLabel("Email and phone", { exact: true }))).check();
  await page.getByLabel(/Reason/).fill("PRIVATE HISTORY REASON");
  await page.getByRole("button", { name: "Send request" }).click();
  await page.getByRole("link", { name: "View request", exact: true }).click();
  const contactPath = new URL(page.url()).pathname;
  await expect(await soon(page.getByRole("button", { name: "Withdraw request" }))).toBeVisible();
  await signInTo(page, adultLogin, contactPath);
  await page.getByRole("button", { name: "Approve sharing" }).click();
  await expect(page.getByRole("button", { name: "Revoke access" })).toBeVisible();
  await signInTo(page, leaderLogin, contactPath);
  await page.getByRole("button", { name: "Reveal contact details" }).click();
  await expect(page.getByRole("region", { name: "Revealed contact details" })).toContainText(email(adultLogin));
  await page.getByRole("button", { name: "Close contact details" }).click();
  const { grants } = await (await page.request.get("/api/v1/contact-grants")).json();
  const grant = grants.find((g: { subjectUserId: number }) => g.subjectUserId === adult.id);
  await signInTo(page, adultLogin, contactPath);
  await page.getByRole("button", { name: "Revoke access" }).click();
  await signIn(page, leaderLogin);
  const csrf = (await (await page.request.get("/api/v1/auth/csrf")).json()).csrfToken;
  const refusal = await page.request.post(`/api/v1/contact-grants/${grant.id}/reveal`, { headers: { "X-CSRF-Token": csrf }, data: { subjectUserId: adult.id, contextType: "group", contextId: group.id, fields: "both" } });
  expect(refusal.ok()).toBe(false);

  const personId = String(leader.id);
  const historyPath = `/admin/users/${personId}/contact-history`;
  await signInTo(page, adminName, "/admin?tab=users");
  await expect(page.getByRole("button", { name: `More for History Leader ${suffix}` })).toBeVisible();
  await capture(page, "admin-users");
  await fromMore(page, "Contact access history", `More for History Leader ${suffix}`);
  await expect(page).toHaveURL(new RegExp(`${historyPath}$`));
  await expect(page.getByRole("heading", { name: "Contact access history", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Contact details revealed", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Contact attempt refused", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Group member added", exact: true })).toBeVisible();
  const json: ContactHistoryResponse = await (await page.request.get(`/api/v1${historyPath}`)).json();
  expect(json.entries.map((e) => e.action)).toEqual(expect.arrayContaining(["contact.requested", "contact.decision", "contact.reveal", "contact.refused", "membership.added"]));
  const serialized = JSON.stringify(json);
  for (const secret of [email(adultLogin), "2085550197", "PRIVATE HISTORY REASON", "password1234"]) expect(serialized).not.toContain(secret);
  for (const e of json.entries) expect(typeof e.id).toBe("string");
  save("actual-history", json); save("domain-ids", { personId, adultId: String(adult.id), groupId: String(group.id) });
  await capture(page, "loaded");

  // Purpose, action and dates sit under "Filters" (DEC-99).
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await page.getByLabel("Direct contact", { exact: true }).check();
  await page.getByLabel("Action (optional)").selectOption("contact.reveal");
  await page.getByLabel("From (optional)").fill("2020-01-01");
  await page.getByLabel("Until (optional)").fill("2099-01-01");
  const filteredRequest = page.waitForRequest((request) => {
    const url = new URL(request.url());
    return request.method() === "GET" && url.pathname === `/api/v1${historyPath}` && url.searchParams.get("action") === "contact.reveal";
  });
  await page.getByRole("button", { name: "Apply filters" }).focus(); await page.keyboard.press("Enter");
  const filteredURL = new URL((await filteredRequest).url());
  expect(filteredURL.searchParams.get("from")).toBe("2020-01-01T07:00:00.000Z");
  expect(filteredURL.searchParams.get("until")).toBe("2099-01-02T07:00:00.000Z");
  await expect(page.getByRole("listitem")).toHaveCount(1);
  await expect(page.getByRole("heading", { name: "Contact details revealed", exact: true })).toBeVisible();
  await capture(page, "filtered");
  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("button", { name: "Export all matching CSV" }).click();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toBe(`contact-history-${personId}.csv`);
  const csvPath = await download.path(); expect(csvPath).not.toBeNull();
  const csv = readFileSync(csvPath!, "utf8");
  expect(csv.split("\r\n")[0]).toBe("auditId,at,action,actorId,requesterId,subjectUserId,purpose,requestId,accessId,contextType,contextId,occurrenceId,fields,outcome,party,decision");
  expect(csv).toContain("contact.reveal"); expect(csv).not.toContain("contact.requested");
  for (const secret of [email(adultLogin), "2085550197", "PRIVATE HISTORY REASON", "password1234"]) expect(csv).not.toContain(secret);
  if (proof) writeFileSync(`${proof}/actual-history.csv`, csv);

  await page.getByLabel("From (optional)").fill("2090-01-01");
  await page.getByRole("button", { name: "Apply filters" }).click();
  await expect(page.getByText("No matching history", { exact: true })).toBeVisible(); await capture(page, "empty");

  const malformedRFC3339 = await page.request.get(`/api/v1${historyPath}?from=not-a-date`);
  expect(malformedRFC3339.status()).toBe(400);
  expect(await malformedRFC3339.text()).not.toContain("entries");

  let reversedRangeSubmissions = 0;
  await page.route((url) => url.pathname === `/api/v1${historyPath}`, async (route) => {
    reversedRangeSubmissions++;
    await route.continue();
  });
  const fromDate = page.getByLabel("From (optional)");
  const untilDate = page.getByLabel("Until (optional)");
  await fromDate.fill("2090-01-02");
  await expect(untilDate).toHaveAttribute("min", "2090-01-02");
  await untilDate.fill("2090-01-01");
  await page.getByRole("button", { name: "Apply filters" }).click();
  const rangeError = page.getByRole("paragraph").filter({ hasText: "Choose a value within the limits shown above." });
  await expect(rangeError).toBeVisible();
  await expect(rangeError).toMatchAriaSnapshot(`- paragraph: Choose a value within the limits shown above.`);
  const rangeErrorID = await rangeError.getAttribute("id");
  expect(rangeErrorID).toBeTruthy();
  expect((await untilDate.getAttribute("aria-describedby"))?.split(/\s+/)).toContain(rangeErrorID);
  await expect(untilDate).toHaveAccessibleDescription("In church time. This whole date is included; choose From or a later date. Choose a value within the limits shown above.");
  await expect(untilDate).toHaveAttribute("aria-invalid", "true");
  await expect(untilDate).toBeFocused();
  await expect(fromDate).toHaveValue("2090-01-02");
  await expect(untilDate).toHaveValue("2090-01-01");
  expect(reversedRangeSubmissions).toBe(0);
  await expect(page.getByText("No matching history", { exact: true })).toBeVisible();
  await capture(page, "error");

  // Retention uses the manual field, not a live-account lookup or test route.
  await (await apiAs(request, adminName))("DELETE", `/admin/users/${adult.id}`);
  await page.goto("/admin/contact-history");
  await (await soon(page.getByText("Look up a deleted account", { exact: true }))).click();
  const savedPersonID = page.getByLabel("Saved person ID", { exact: true });
  await savedPersonID.fill(String(adult.id));
  await page.getByRole("button", { name: "Show history" }).click();
  await expect(page.getByRole("heading", { name: "Contact requested", exact: true })).toBeVisible();
  const retained = await (await page.request.get(`/api/v1/admin/users/${String(adult.id)}/contact-history`)).json(); save("deleted-retained", retained);
  expect(retained.entries.some((e: { action: string }) => e.action === "contact.requested")).toBe(true);
  await savedPersonID.fill("9223372036854775807");
  await page.getByRole("button", { name: "Show history" }).click();
  await expect(page.getByText("No matching history", { exact: true })).toBeVisible();

  await signInTo(page, leaderLogin, historyPath);
  await expect(page.getByText("This page is for church admins only.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Export all matching CSV" })).toHaveCount(0);
  const refusedJSON = await page.request.get(`/api/v1${historyPath}`);
  const refusedCSV = await page.request.get(`/api/v1${historyPath}.csv`);
  expect(refusedJSON.status()).toBe(403); expect(refusedCSV.status()).toBe(403);
  expect(await refusedJSON.text()).not.toContain("entries"); expect(refusedCSV.headers()["content-type"]).toContain("application/json");
  save("nonadmin-denial", { jsonStatus: refusedJSON.status(), csvStatus: refusedCSV.status(), csvBody: await refusedCSV.json() });
});
