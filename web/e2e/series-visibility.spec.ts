import { expect, test } from "@playwright/test";
import { apiAs, signIn } from "./helpers";

function futureDate(days: number): string {
  return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
}

// Regression (Grier, Oct 14): a second recurring series copies only the
// group's visibility, never the first series' override.
test("a second series starts at the group's visibility", async ({ page, request }) => {
  test.setTimeout(30_000);
  const admin = await apiAs(request, "grier");
  const { group } = (await admin("POST", "/groups", {
    name: "Bell choir", description: "", tags: [], location: "Loft", visibility: "private", memberLimit: null,
  })) as { group: { id: number } };

  await signIn(page, "grier");
  const schedule = async (title: string, days: number) => {
    await page.goto(`/groups/${group.id}/events/new`);
    await page.getByLabel("Title (optional)").fill(title);
    await page.getByLabel("Date").fill(futureDate(days));
    await page.getByLabel("Start time").fill("19:00");
    await page.getByText("Repeat weekly").click();
    await page.getByRole("button", { name: "Mon", exact: true }).click();
    await page.getByRole("button", { name: "Schedule event" }).click();
    await expect(page).toHaveURL(new RegExp(`/groups/${group.id}$`));
  };
  await schedule("Rehearsal", 3);
  const firstControl = page.getByLabel("Event visibility").first();
  await firstControl.selectOption("public");
  await page.getByRole("button", { name: "Save visibility" }).first().click();
  await expect(page.getByText("A leader opened this event wider than the group.")).toBeVisible();

  await schedule("Sectionals", 4);
  const evs = (await admin("GET", `/groups/${group.id}`)) as {
    upcomingEvents: { eventId: number; title: string; visibility: string; leaderOpened: boolean }[];
  };
  const second = evs.upcomingEvents.filter((o) => o.title === "Sectionals");
  expect(second.length).toBeGreaterThan(0);
  for (const o of second) {
    expect(o.visibility).toBe("private");
    expect(o.leaderOpened).toBe(false);
  }
  await expect(page.getByText("A leader opened this event wider than the group.")).toHaveCount(1);
});
