import { expect, test } from "@playwright/test";
import { apiAs, signIn } from "./helpers";

// UI makeover PR 6 (DEC-63): one primary action per view with the rest in
// a More menu, a series as its next date plus "N more", attendance counts
// first with names behind an expand, and a segmented Going / Maybe /
// Can't go where Can't go never lists anyone.
async function seriesGroup(request: Parameters<typeof apiAs>[0]) {
  const admin = await apiAs(request, "grier");
  const { groups } = (await admin("GET", "/groups")) as { groups: { id: number; name: string }[] };
  const room = groups.find((g) => g.name === "Room 201 class")!;
  const lee = await apiAs(request, "lee");
  const d = new Date(Date.now() + 9 * 864e5).toISOString().slice(0, 10);
  const { event } = (await lee("POST", `/groups/${room.id}/events`, {
    title: "Segmented night", startsLocal: `${d}T19:00`, durationMinutes: 60, tzid: "America/Boise",
    location: "Room 201", recurrenceRule: "FREQ=WEEKLY;COUNT=3",
  })) as { event: { id: number } };
  return { room, event };
}

test("the leader group page at 390 shows one primary action and a keyboard-friendly More menu", async ({ page, request }) => {
  const { room } = await seriesGroup(request);
  // A 30-week series: the group page loads 90 days, so it must not claim 29.
  const lee = await apiAs(request, "lee");
  const d = new Date(Date.now() + 10 * 864e5).toISOString().slice(0, 10);
  await lee("POST", `/groups/${room.id}/events`, {
    title: "Long series", startsLocal: `${d}T18:00`, durationMinutes: 60, tzid: "America/Boise",
    location: "Room 201", recurrenceRule: "FREQ=WEEKLY;COUNT=30",
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page, "lee");
  await page.goto(`/groups/${room.id}`);
  await expect(page.getByRole("heading", { name: "Room 201 class" })).toBeVisible();

  // Header: Schedule event is the primary; Edit group and the cover live in More.
  await expect(page.getByRole("link", { name: "Schedule event" })).toBeVisible();
  for (const name of ["Edit group", "Cover photo", "Leave group", "Edit this date", "Edit series"]) {
    await expect(page.getByRole("button", { name, exact: true })).toHaveCount(0);
  }

  // The series is one card: its next date plus "2 more dates in the next 90 days".
  const card = page.locator("div.bg-surface").filter({ has: page.getByText("Segmented night", { exact: true }) });
  await expect(card).toHaveCount(1);
  await expect(card.getByRole("link", { name: "2 more dates in the next 90 days" })).toBeVisible();
  const long = page.locator("div.bg-surface").filter({ has: page.getByText("Long series", { exact: true }) });
  await expect(long.getByRole("link", { name: /^1[0-2] more dates in the next 90 days$/ })).toBeVisible();

  // The menu works from the keyboard and opens fully on screen.
  const more = page.getByRole("button", { name: "More group actions" });
  await more.focus();
  await page.keyboard.press("Enter");
  await expect(more).toHaveAttribute("aria-expanded", "true");
  const menu = page.getByRole("menu", { name: "More group actions" });
  await expect(menu.getByRole("menuitem", { name: "Edit group" })).toBeFocused();
  await expect(menu).toBeInViewport({ ratio: 1 });
  for (const item of await menu.getByRole("menuitem").all()) expect((await item.boundingBox())!.height).toBeGreaterThanOrEqual(44);
  await page.keyboard.press("ArrowDown");
  await expect(menu.getByRole("menuitem", { name: "Change cover photo" })).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(menu).toBeHidden();
  await expect(more).toBeFocused();
  await expect(more).toHaveAttribute("aria-expanded", "false");

  // A date's actions open from its own menu.
  await card.getByRole("button", { name: /^More for Segmented night/ }).click();
  await expect(page.getByRole("menu")).toBeInViewport({ ratio: 1 });
  await page.getByRole("menuitem", { name: "Edit this date" }).click();
  await expect(card.getByLabel("Title").first()).toBeVisible();
});

test("RSVP is a segmented control, counts come first, and Can't go never lists anyone", async ({ page, request }) => {
  const { event } = await seriesGroup(request);
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page, "sam");
  await page.goto(`/events/${event.id}`);
  const first = page.locator("div.bg-surface").filter({ has: page.getByRole("group", { name: "Your answer" }) }).first();
  const answer = first.getByRole("group", { name: "Your answer" });
  for (const name of ["Going", "Maybe", "Can't go"]) {
    const b = answer.getByRole("button", { name });
    await expect(b).toHaveAttribute("aria-pressed", "false");
    expect((await b.boundingBox())!.height).toBeGreaterThanOrEqual(44);
  }
  await expect(first.getByText("No one yet")).toBeVisible();

  await answer.getByRole("button", { name: "Maybe" }).click();
  await expect(answer.getByRole("button", { name: "Maybe" })).toHaveAttribute("aria-pressed", "true");
  await expect(first.getByText("0 going · 1 maybe")).toBeVisible();
  const show = first.getByRole("button", { name: "Show who's coming" });
  await expect(show).toHaveAttribute("aria-expanded", "false");
  await show.click();
  await expect(first.getByRole("button", { name: "Hide names" })).toHaveAttribute("aria-expanded", "true");
  await expect(first.getByRole("listitem").filter({ hasText: "Sam Member (maybe)" })).toBeVisible();

  await answer.getByRole("button", { name: "Can't go" }).click();
  await expect(answer.getByRole("button", { name: "Can't go" })).toHaveAttribute("aria-pressed", "true");
  await expect(first.getByText("No one yet")).toBeVisible();
  await expect(first.getByText(/can.t go/i).and(first.locator(":not(button)"))).toHaveCount(0);
  await expect(first.getByText("Sam Member")).toHaveCount(0);
});
