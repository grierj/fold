import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { mkdir, writeFile } from "node:fs/promises";
import { apiAs, email, fromMore, PASSWORD, signIn } from "./helpers";
import { measure } from "./ui-measure.mjs";

test("name picker keeps safe identities, teen guidance and existing adult add controls", async ({ page, request }) => {
  test.setTimeout(120_000);
  const suffix = Date.now();
  const patName = `Picker Pat ${suffix}`;
  const teenName = `Picker Teen ${suffix}`;
  const leaderLogin = `picker-leader-${suffix}`;
  let admin = await apiAs(request, "grier");
  const create = async (login: string, name: string, teen = false) => (await admin("POST", "/admin/users", {
    email: email(login), name, password: PASSWORD, ageBand: teen ? "teen" : "adult",
    ...(teen ? { birthDate: `${new Date().getUTCFullYear() - 15}-01-01` } : {}), phone: "2085550149",
  })).user;
  const leader = await create(leaderLogin, "Picker Leader");
  const adult = await create(`picker-adult-${suffix}`, patName);
  const other = await create(`picker-other-${suffix}`, patName);
  const teen = await create(`picker-teen-${suffix}`, teenName, true);
  await admin("POST", `/admin/users/${teen.id}/teen-consents`, { scope: "account", status: "granted", evidenceText: "Parent consent recorded for picker test" });
  const group = async (name: string, visibility = "public") => (await admin("POST", "/groups", { name: `${name} ${suffix}`, description: "Picker proof", tags: [], location: "Hall", visibility, memberLimit: null })).group;
  const destination = await group("Picker group");
  await admin("POST", `/admin/groups/${destination.id}/members`, { userId: leader.id, role: "leader" });
  const shared = await group("Picker Choir");
  for (const userId of [leader.id, adult.id]) await admin("POST", `/admin/groups/${shared.id}/members`, { userId, role: "member" });
  const visible = await group("Picker Walking");
  await admin("POST", `/admin/groups/${visible.id}/members`, { userId: adult.id });
  const hidden = await group("Hidden care circle", "invite_only");
  await admin("POST", `/admin/groups/${hidden.id}/members`, { userId: other.id });
  await admin("PUT", `/groups/${destination.id}`, { memberLimit: 2 });
  const startsLocal = `${new Date(Date.now() + 7 * 864e5).toISOString().slice(0, 10)}T19:00`;
  const { event } = await admin("POST", `/groups/${destination.id}/events`, { title: "Picker event", startsLocal, durationMinutes: 60, tzid: "America/Boise", capacity: 1, childcareAvailable: true, childcareCapacity: 2 });
  const hiddenAdult = await apiAs(request, `picker-adult-${suffix}`);
  await hiddenAdult("PUT", "/me", { hideContactFromLeaders: true });
  admin = await apiAs(request, "grier");

  const results: unknown[] = [];
  const proof = process.env.SCREENS_DIR;
  if (proof) await mkdir(proof, { recursive: true });
  async function capture(name: string, p: Page = page) {
    for (const width of [390, 768, 1280]) {
      await p.setViewportSize({ width, height: 900 });
      await p.evaluate(() => window.scrollTo(0, 0));
      await p.waitForTimeout(100);
      const layout = await p.evaluate(measure);
      expect(layout.overflow, name).toBe(0);
      expect(layout.clipped, name).toEqual([]);
      expect(layout.cutOff, name).toEqual([]);
      expect(layout.under24, name).toEqual([]);
      const { violations } = await new AxeBuilder({ page: p }).withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"]).analyze();
      expect(violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => ({ id: v.id, targets: v.nodes.map((n) => n.target) })), `${name}@${width}`).toEqual([]);
      const rows = await p.getByRole("listbox", { name: "People" }).getByRole("option").evaluateAll((nodes) => nodes.map((n) => n.getBoundingClientRect().height));
      expect(rows.every((height) => height >= 44), name).toBe(true);
      if (proof) await p.screenshot({ path: `${proof}/${name}-${width}.png`, fullPage: true });
      results.push({ name, width, layout, rows, seriousOrCritical: [] });
    }
  }

  await signIn(page, leaderLogin);
  page.on("dialog", (dialog) => dialog.accept());
  await page.goto(`/groups/${destination.id}`);
  const membersTab = page.getByRole("tablist", { name: "Group sections", exact: true })
    .getByRole("tab", { name: "Members", exact: true });
  await membersTab.click();
  await expect(membersTab).toHaveAttribute("aria-selected", "true");
  const membersPanel = page.getByRole("tabpanel", { name: "Members", exact: true });
  await expect(membersPanel).toBeVisible();
  const picker = page.getByRole("combobox", { name: "Find a person by name" });
  const searchReply = page.waitForResponse((r) => r.url().includes("/people/search?") && r.request().method() === "GET");
  await picker.fill(patName);
  const search = await (await searchReply).json();
  expect(search.people).toHaveLength(2);
  for (const person of search.people) {
    expect(person).not.toHaveProperty("email"); expect(person).not.toHaveProperty("phone"); expect(person).not.toHaveProperty("id");
    expect(JSON.stringify(person)).not.toContain(hidden.name);
  }
  await expect(page.getByRole("status")).toContainText("2 people found");
  await expect(page.getByRole("listbox", { name: "People" }).getByRole("option").filter({ hasText: shared.name })).toContainText(visible.name);
  await picker.press("ArrowDown");
  await expect(picker).toHaveAttribute("aria-activedescendant", /person-0$/);
  await capture("group-picker-keyboard");
  await picker.press("Escape"); await expect(picker).toHaveAttribute("aria-expanded", "false");
  await picker.press("ArrowDown"); await picker.press("Enter");
  const memberPost = page.waitForRequest((r) => r.url().endsWith(`/groups/${destination.id}/members`) && r.method() === "POST");
  await page.getByRole("button", { name: "Add member", exact: true }).click();
  const memberBody = (await memberPost).postDataJSON();
  expect(memberBody).toEqual({ selectionId: expect.any(String), overLimit: false });
  await expect(page.getByText(patName, { exact: true })).toBeVisible();
  await capture("group-adult-added");
  await picker.fill(teenName); await page.getByRole("listbox", { name: "People" }).getByRole("option").click();
  await expect(page.getByRole("status")).toContainText("Adding a teen needs their approval and an admin's approval.");
  await expect(page.getByRole("button", { name: "Add member", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Ask to add", exact: true })).toBeEnabled();
  await capture("group-teen-guidance");

  await page.goto(`/events/${event.id}`); await fromMore(page, "Manage sign-ups");
  await picker.fill(patName); await expect(page.getByRole("listbox", { name: "People" }).getByRole("option")).toHaveCount(2);
  await expect(page.getByRole("listbox", { name: "People" }).getByRole("option").first()).not.toContainText("2085550149");
  await capture("event-picker");
  await page.getByRole("listbox", { name: "People" }).getByRole("option").first().click();
  const eventPost = page.waitForRequest((r) => r.url().endsWith("/registrations/add") && r.method() === "POST");
  await page.getByRole("button", { name: "Add", exact: true }).click();
  expect((await eventPost).postDataJSON()).toEqual({ date: startsLocal.slice(0, 10), selectionId: expect.any(String) });
  await expect(page.getByRole("region", { name: "Manage sign-ups" })).toContainText(patName);
  await capture("event-adult-added");

  await signIn(page, "grier"); await page.goto("/admin?tab=groups");
  const card = page.getByRole("link", { name: destination.name, exact: true }).locator("../../..");
  await card.getByRole("button", { name: "Manage members" }).click();
  await picker.fill(teenName); await expect(page.getByRole("listbox", { name: "People" }).getByRole("option")).toHaveCount(1);
  await expect(page.getByRole("listbox", { name: "People" }).getByRole("option")).toContainText("Teen");
  expect(await page.getByRole("listbox", { name: "People" }).getByRole("option").locator("img").count()).toBe(0);
  await capture("admin-picker-teen");
  await page.getByRole("listbox", { name: "People" }).getByRole("option").click();
  await expect(card.getByRole("button", { name: "Add", exact: true })).toHaveCount(0);
  await expect(card.getByRole("button", { name: "Ask to add", exact: true })).toBeEnabled();
  await capture("admin-teen-guidance");
  await picker.fill(patName); await page.getByRole("listbox", { name: "People" }).getByRole("option").last().click();
  const adminPost = page.waitForRequest((r) => r.url().endsWith(`/admin/groups/${destination.id}/members`) && r.method() === "POST");
  await card.getByRole("button", { name: "Add", exact: true }).click();
  expect((await adminPost).postDataJSON()).toEqual({ selectionId: expect.any(String), role: "member", overLimit: false });
  await expect(card.getByText(patName, { exact: true })).toHaveCount(2);
  await capture("admin-adult-added");
  if (proof) await writeFile(`${proof}/results.json`, JSON.stringify({ feature: "name-search", entry: "group members / event Manage sign-ups / admin Manage members", results }, null, 2));
});
