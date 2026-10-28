import { expect, test } from "@playwright/test";
import { apiAs, signIn, fromMore } from "./helpers";

// DEC-57: every time on screen is in the browser's zone. This browser is
// New York and the event is 10:30 PM in Boise, which is 12:30 AM the next
// day here, so the date badge moves too. US zones change clocks on the same
// dates, so this holds in any week.
test.use({ locale: "en-US", timezoneId: "America/New_York" });

function futureDate(days: number): string {
  return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
}

test("event times follow the browser's zone, with one 'held in' line on the event page", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  const { group } = (await admin("POST", "/groups", {
    name: "Late club", description: "", tags: [], location: "", visibility: "public", memberLimit: null,
  })) as { group: { id: number } };
  const date = futureDate(9);
  const { event } = (await admin("POST", `/groups/${group.id}/events`, {
    title: "Night watch", startsLocal: `${date}T22:30`, durationMinutes: 60, tzid: "America/Boise",
    location: "", recurrenceRule: "",
  })) as { event: { id: number } };
  const nextDay = new Date(`${date}T12:00:00Z`);
  nextDay.setUTCDate(nextDay.getUTCDate() + 1);
  const badgeDay = String(nextDay.getUTCDate());

  await signIn(page, "grier");
  await page.goto(`/groups/${group.id}`);
  const card = page.locator("div.rounded-lg", { hasText: "Night watch" }).first();
  await expect(card.getByText(/^12:30 AM/)).toBeVisible();
  await expect(card.getByText(badgeDay, { exact: true })).toBeVisible();
  await expect(card.getByText(/Held in/)).toHaveCount(0);

  await page.goto(`/events/${event.id}`);
  await expect(page.getByText(/^12:30 AM/)).toBeVisible();
  await expect(page.getByText(/^Held in Mountain time \(\w{3}, 10:30 PM M[DS]T\)$/)).toBeVisible();

  // The per-date editor edits the event's own clock and says which zone.
  await fromMore(page, "Edit this date");
  await expect(page.getByText(/Times are in Mountain time\./)).toBeVisible();
  await expect(page.getByLabel("Start time")).toHaveValue("22:30");
});

test("the profile keeps a time zone for emails", async ({ page }) => {
  await signIn(page, "grier");
  await page.goto("/profile/details");
  await expect(page.getByLabel("Time zone for emails")).not.toHaveValue("");
});
