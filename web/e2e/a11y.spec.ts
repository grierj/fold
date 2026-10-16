import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { apiAs, signIn, signOut } from "./helpers";

// axe on the main pages at phone and desktop widths (UI makeover PR 3).
// Serious and critical issues fail; minor and moderate ones are listed in
// the test output so they can be fixed as pages are redone.
async function scan(page: Page, name: string) {
  for (const width of [390, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    const { violations } = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
      .analyze();
    const bad = violations.filter((v) => v.impact === "serious" || v.impact === "critical");
    for (const v of violations.filter((v) => !bad.includes(v))) {
      console.log(`axe ${v.impact} ${name}@${width}: ${v.id} (${v.nodes.length})`);
    }
    expect.soft(
      bad.map((v) => `${v.impact} ${v.id}: ${v.help} — ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`),
      `${name} at ${width}px`,
    ).toEqual([]);
  }
}

test("main pages have no serious or critical axe issues", async ({ page, request }) => {
  test.setTimeout(120_000);
  const admin = await apiAs(request, "grier");
  const { groups } = (await admin("GET", "/groups")) as { groups: { id: number; name: string }[] };
  const room = groups.find((g) => g.name === "Room 201 class")!;

  await page.goto("/");
  await expect(page.getByText("Room 201 class").first()).toBeVisible();
  await scan(page, "home signed out");
  await page.goto("/login");
  await scan(page, "login");

  await signIn(page, "grier");
  await page.goto("/");
  await expect(page.getByText("Room 201 class").first()).toBeVisible();
  await scan(page, "home");
  await page.goto(`/groups/${room.id}`);
  await expect(page.getByRole("heading", { name: "Room 201 class" })).toBeVisible();
  await scan(page, "group");
  await page.goto("/groups/new");
  await expect(page.getByLabel("Group name")).toBeVisible();
  await scan(page, "new group");
  await page.goto("/profile");
  await expect(page.getByLabel("Time zone for emails")).not.toHaveValue("");
  await scan(page, "profile");
  await page.goto("/notifications");
  await page.waitForLoadState("networkidle");
  await scan(page, "notifications");
  for (const tab of ["approvals", "groups", "users", "invite"]) {
    await page.goto(tab === "approvals" ? "/admin" : `/admin?tab=${tab}`);
    await page.waitForLoadState("networkidle");
    await scan(page, `admin ${tab}`);
  }
  await signOut(page);
});
