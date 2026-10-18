import { expect, test } from "@playwright/test";
import { apiAs, signIn, fromMore } from "./helpers";

function futureDate(days: number): string {
  return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
}

// DEC-51: visibility is asked when the event is created, defaulting to the
// group's value. There is no standalone control on the event list; it lives
// in the "Edit series" section.
test("visibility is chosen at creation and edited inside Edit series", async ({ page, request }) => {
  test.setTimeout(30_000);
  const admin = await apiAs(request, "grier");
  const { group } = (await admin("POST", "/groups", {
    name: "Bell choir", description: "", tags: [], location: "Loft", visibility: "private", memberLimit: null,
  })) as { group: { id: number } };

  await signIn(page, "grier");
  const schedule = async (title: string, days: number, choice?: string) => {
    await page.goto(`/groups/${group.id}/events/new`);
    const vis = (v: string) => page.getByRole("group", { name: "Who can see this event" }).locator(`input[value="${v}"]`);
    await expect(vis("private")).toBeChecked();
    await page.getByLabel("Title (optional)").fill(title);
    await page.getByLabel("Date").fill(futureDate(days));
    await page.getByLabel("Start time").fill("19:00");
    if (choice) {
      await vis(choice).check();
      await expect(page.getByText("Only leaders will see who signed up")).toBeVisible();
      await page.getByLabel("Reason (optional)").fill("Open rehearsal");
    }
    // Turning on "Repeat weekly" selects the start date's weekday. Clicking
    // a fixed day here toggled it off whenever that date was a Monday.
    await page.getByText("Repeat weekly").click();
    await page.getByRole("button", { name: "Schedule event" }).click();
    await expect(page).toHaveURL(new RegExp(`/groups/${group.id}$`));
  };
  await schedule("Rehearsal", 3, "public");
  await schedule("Sectionals", 4);
  await expect(page.getByRole("group", { name: "Who can see this event" })).toHaveCount(0);

  const evs = (await admin("GET", `/groups/${group.id}`)) as {
    upcomingEvents: { title: string; visibility: string; leaderOpened: boolean }[];
  };
  for (const o of evs.upcomingEvents) {
    if (o.title === "Rehearsal") expect([o.visibility, o.leaderOpened]).toEqual(["public", true]);
    if (o.title === "Sectionals") expect([o.visibility, o.leaderOpened]).toEqual(["private", false]);
  }

  await fromMore(page, "Edit series");
  await expect(page.getByRole("group", { name: "Who can see this event" })).toHaveCount(1);
});
