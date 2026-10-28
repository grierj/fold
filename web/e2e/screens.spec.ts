import { expect, test, type Page } from "@playwright/test";
import { apiAs, signIn, signOut } from "./helpers";

// Screenshot capture for upgrade and styling PRs (M1.12, UI makeover).
// Skipped unless SCREENS_DIR is set. Run it alone on a fresh database so
// two runs see the same data:
//   SCREENS_DIR=/tmp/shots/before npx playwright test seed screens
// then compare two directories with e2e/compare-screens.mjs.
const dir = process.env.SCREENS_DIR;
const widths = [390, 768, 1280];

test.use({ locale: "en-US", timezoneId: "America/Boise" });
test.skip(!dir, "set SCREENS_DIR to capture screenshots");

async function shoot(page: Page, name: string) {
  for (const w of widths) {
    await page.setViewportSize({ width: w, height: 900 });
    await page.waitForTimeout(150);
    await page.screenshot({ path: `${dir}/${name}-${w}.png`, fullPage: true, animations: "disabled", caret: "hide" });
  }
}

test("capture the main pages", async ({ page, request }) => {
  test.setTimeout(120_000);
  const admin = await apiAs(request, "grier");
  const { groups } = (await admin("GET", "/groups")) as { groups: { id: number; name: string }[] };
  const room = groups.find((g) => g.name === "Room 201 class")!;
  const { event } = (await admin("POST", `/groups/${room.id}/events`, {
    title: "Thursday class", startsLocal: "2026-11-19T19:00", durationMinutes: 60, tzid: "America/Boise",
    location: "Room 201", recurrenceRule: "FREQ=WEEKLY;COUNT=3",
  })) as { event: { id: number } };

  await page.goto("/");
  await expect(page.getByText("Room 201 class").first()).toBeVisible();
  await shoot(page, "home-signed-out");
  await page.goto("/login");
  await shoot(page, "login");

  await signIn(page, "grier");
  await page.goto("/");
  await expect(page.getByText("Room 201 class").first()).toBeVisible();
  await shoot(page, "home");
  await page.goto(`/groups/${room.id}`);
  await expect(page.getByText("Thursday class").first()).toBeVisible();
  await shoot(page, "group");
  await page.goto(`/events/${event.id}`);
  await expect(page.getByText("Thursday class").first()).toBeVisible();
  await shoot(page, "event");
  await page.goto("/groups/new");
  await expect(page.getByLabel("Group name")).toBeVisible();
  await shoot(page, "new-group");
  await page.goto("/profile");
  await expect(page.getByRole("heading", { name: "Your profile", level: 1 })).toBeVisible();
  await shoot(page, "profile");
  await page.goto("/notifications");
  await shoot(page, "notifications");
  for (const tab of ["approvals", "groups", "users", "invite"]) {
    await page.goto(tab === "approvals" ? "/admin" : `/admin?tab=${tab}`);
    await page.waitForLoadState("networkidle");
    await shoot(page, `admin-${tab}`);
  }
  await signOut(page);
});

test("capture household selected answers, metadata editor and long care disclosures", async ({ page, request }) => {
  const { uiPages, open } = await import("./ui-pages");
  for (const ui of (await uiPages(request)).filter((p) => ["profile-household", "event", "event-leader"].includes(p.id))) {
    await signIn(page, ui.who!);
    await open(page, ui);
    await shoot(page, ui.id === "profile-household" ? "household-profile" : `household-${ui.id}`);
    if (ui.id === "event-leader") {
      await page.getByRole("button", { name: "Show full care notes for Kit" }).click();
      await shoot(page, "household-care-expanded");
    }
  }
});
