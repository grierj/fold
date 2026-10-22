import { expect, test, type Page } from "@playwright/test";
import { apiAs, email, PASSWORD, signIn } from "./helpers";

// Run with E2E_APP_ENV=dev to exercise the real mail and acceptance path.
// Production-mode runs keep the outbox closed.
async function latestInvite(page: Page, address: string): Promise<string> {
  let link = "";
  await expect.poll(async () => {
    const response = await page.request.get("/dev/outbox");
    const html = await response.text();
    const sections = html.split("<section");
    const section = sections.find((part) => part.includes(`To: ${address}`));
    link = section?.match(/https?:[^\s<]+\/invite\?token=[A-Za-z0-9_-]+/)?.[0] ?? "";
    return link;
  }).not.toBe("");
  return new URL(link).pathname + new URL(link).search;
}

// With SCREENS_DIR set, capture a state at 390 and 1280 px.
async function shoot(page: import("@playwright/test").Page, name: string) {
  const dir = process.env.SCREENS_DIR;
  if (!dir) return;
  for (const width of [390, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    await page.screenshot({ path: `${dir}/${name}-${width}.png`, fullPage: true });
  }
  await page.setViewportSize({ width: 1280, height: 900 });
}

test("household adult invitation transfers sign-ups when the account is ready", async ({ page, request }) => {
  const kind = "adult";
  test.skip((await request.get("/dev/outbox")).status() !== 200, "Requires E2E_APP_ENV=dev for the mail outbox");
  const admin = await apiAs(request, "grier");
  const account = `conversion-${kind}-parent`;
  await admin("POST", "/admin/users", { name: `Conversion ${kind} parent`, email: email(account), password: PASSWORD, role: "member" });
  const { group } = await admin("POST", "/groups", { name: `Conversion ${kind} group`, description: "Household conversion", visibility: "public", tags: [] });
  const startsLocal = new Date(Date.now() + 7 * 86400_000).toISOString().slice(0, 16);
  const { event } = await admin("POST", `/groups/${group.id}/events`, { title: `Conversion ${kind} event`, startsLocal, durationMinutes: 60, tzid: "UTC", capacity: 1, waitlistEnabled: true });
  const parent = await apiAs(page.request, account);
  const { person } = await parent("POST", "/me/household/people", { firstName: `Conversion ${kind}`, kind, birthDate: "1990-01-01" });
  await parent("POST", "/me/household/people", { firstName: "Household teen", kind: "teen", birthDate: "2011-01-01" });
  const { occurrences } = await parent("GET", `/events/${event.id}`);
  const result = await parent("PUT", `/events/${event.id}/registrations`, { date: occurrences[0].date, people: [{ personId: person.id, status: "going" }] });
  const registrationId = result.people[0].registrationId;
  await signIn(page, account);
  await page.goto("/profile");
  await expect(page.getByRole("link", { name: "Accounts and household help" })).toHaveAttribute("href", "/help/members");
  const help = await page.request.get("/help/members");
  expect(await help.text()).toContain("Create a separate account");
  await expect(page.getByText("Teens can get their own account once parent consent is available.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Create their own account" })).toHaveCount(1);
  await page.getByRole("button", { name: "Create their own account" }).click();
  const destination = email(`conversion-${kind}-own`);
  await page.getByLabel("Their email address").fill(destination);
  await page.getByRole("button", { name: "Send account invitation" }).click();
  await expect(page.getByText(`Invitation pending: ${destination}`, { exact: false })).toBeVisible();
  await shoot(page, `conversion-${kind}-pending`);
  const first = await latestInvite(page, destination);
  await page.getByRole("button", { name: "Resend invitation", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("Invitation resent");
  const second = await latestInvite(page, destination);
  expect(second).not.toBe(first);
  await page.getByRole("button", { name: "Cancel invitation" }).click();
  await expect(page.getByRole("status")).toContainText("Invitation cancelled");
  await page.getByRole("button", { name: "Create their own account" }).click();
  await page.getByLabel("Their email address").fill(destination);
  await page.getByRole("button", { name: "Send account invitation" }).click();
  await expect(page.getByRole("status")).toContainText("Invitation sent");
  const final = await latestInvite(page, destination);
  await page.context().clearCookies();
  await page.goto(second);
  await expect(page.getByRole("heading", { name: "This link can't be used" })).toBeVisible();
  await shoot(page, `conversion-${kind}-link-used`);
  await page.goto(final);
  await expect(page.getByText(/Your past and upcoming sign-ups will move/)).toBeVisible();
  await shoot(page, `conversion-${kind}-accept`);
  await page.getByLabel("Choose a password").fill(PASSWORD);
  await page.getByRole("button", { name: "Create my account" }).click();
  // Adults wait for admin approval (Warden, PR 111 F1).
  await expect(page.getByRole("heading", { name: "Your account is almost ready" })).toBeVisible();
  await shoot(page, `conversion-${kind}-waiting`);
  expect((await page.request.get("/api/v1/auth/me")).status()).toBe(401);
  await expect(page.getByText(/your household keeps managing your sign-ups/)).toBeVisible();
  await signIn(page, account);
  await page.goto("/profile");
  await expect(page.getByText("Account waiting for an admin")).toBeVisible();
  await expect(page.getByRole("button", { name: `Edit Conversion ${kind}` })).toBeVisible();
  await shoot(page, "conversion-adult-household-awaiting-approval");
  const household = await parent("GET", "/me/household");
  expect(household.household.people).toEqual(expect.arrayContaining([expect.objectContaining({ id: person.id, conversion: expect.objectContaining({ state: "awaiting_approval" }) })]));
  const beforeApproval = await parent("GET", `/events/${event.id}`);
  expect(beforeApproval.occurrences[0].myHouseholdRegistrations).toEqual(expect.arrayContaining([expect.objectContaining({ registrationId, personId: person.id, status: "going" })]));
  await page.goto(`/events/${event.id}`);
  await expect(page.getByText(`Conversion ${kind}`, { exact: true }).first()).toBeVisible();
  await signIn(page, "grier");
  await page.goto("/admin");
  const card = page.getByText(`Conversion ${kind}`, { exact: true }).locator("xpath=ancestor::div[contains(@class,'rounded-lg')][1]");
  await card.getByRole("button", { name: "Approve" }).click();
  await expect(card).toHaveCount(0);
  await signIn(page, `conversion-${kind}-own`);
  expect((await page.request.get("/api/v1/auth/me")).status()).toBe(200);
  const updated = await admin("GET", `/events/${event.id}`);
  expect(updated.occurrences[0].spotsLeft).toBe(0);
  expect(updated.occurrences[0].attendees).toEqual(expect.arrayContaining([expect.objectContaining({ registrationId, name: `Conversion ${kind}`, accountType: kind })]));
  await signIn(page, account); await page.goto("/profile");
  await expect(page.getByRole("button", { name: `Edit Conversion ${kind}` })).toHaveCount(0);
  await admin("POST", "/notifications/read-all", {});
});

test("profile reserves household space while its chunk loads", async ({ page }) => {
  await signIn(page, "sam");
  let release = () => {};
  const ready = new Promise<void>((resolve) => { release = resolve; });
  await page.route("**/HouseholdCard-*.js", async (route) => { await ready; await route.continue(); });
  await page.goto("/profile", { waitUntil: "domcontentloaded" });
  const skeleton = page.getByRole("region", { name: "My household loading" });
  await expect(skeleton).toBeVisible();
  const height = await skeleton.evaluate((node) => node.getBoundingClientRect().height);
  expect(height).toBeGreaterThanOrEqual(300);
  release();
  await expect(page.getByRole("link", { name: "Accounts and household help" })).toBeVisible();
});
