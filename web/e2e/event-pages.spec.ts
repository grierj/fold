import { expect, test } from "@playwright/test";
import type { GroupDetailResponse } from "../src/types";
import { apiAs, signIn } from "./helpers";

// UI makeover PR 6 (DEC-63): one primary action per view with the rest in
// a More menu, a series as its next date plus "See all N dates", attendance counts
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
  // A 30-week series: only loaded dates count, not all 29 later dates.
  const lee = await apiAs(request, "lee");
  const d = new Date(Date.now() + 10 * 864e5).toISOString().slice(0, 10);
  await lee("POST", `/groups/${room.id}/events`, {
    title: "Long series", startsLocal: `${d}T18:00`, durationMinutes: 60, tzid: "America/Boise",
    location: "Room 201", recurrenceRule: "FREQ=WEEKLY;COUNT=30",
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page, "lee");
  await page.goto(`/groups/${room.id}`);
  await expect(page.getByRole("heading", { name: "Room 201 class", level: 1 })).toBeVisible();

  // Header: Schedule event is the primary; Edit group and the cover live in More.
  await expect(page.getByRole("link", { name: "Schedule event" })).toBeVisible();
  for (const name of ["Edit group", "Cover photo", "Leave group", "Edit this date", "Edit series"]) {
    await expect(page.getByRole("button", { name, exact: true })).toHaveCount(0);
  }

  // The series is one card: its next date plus "See all 3 dates" (a bounded series with every date loaded, DEC-99).
  const card = page.getByRole("article", { name: "Segmented night", exact: true });
  await expect(card).toHaveCount(1);
  await expect(card.getByRole("link", { name: "See all 3 dates" })).toBeVisible();
  const long = page.getByRole("article", { name: "Long series", exact: true });
  await expect(long.getByRole("link", { name: /^See (all \d+|more) dates$/ })).toBeVisible();

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
  await expect(menu.getByRole("menuitem", { name: "Email members" })).toBeFocused();
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

  // On the event page, sign-ups are managed from the date's More menu, in
  // their own panel with a labeled full-width field.
  await card.getByRole("link", { name: /See (all \d+|more) dates/ }).click();
  await expect(page.getByRole("region", { name: "Manage sign-ups" })).toHaveCount(0);
  await page.getByRole("button", { name: /^More for / }).first().click();
  await page.getByRole("menuitem", { name: "Manage sign-ups" }).click();
  const panel = page.getByRole("region", { name: "Manage sign-ups" });
  await expect(panel.getByLabel("Add someone")).toBeVisible();
  expect((await panel.getByLabel("Add someone").boundingBox())!.width).toBeGreaterThan(250);
});

test("group dates continue across page boundaries to exhaustion without duplicates", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  const { group } = (await admin("POST", "/groups", {
    name: "Continuation group", visibility: "public", tags: [],
  })) as { group: { id: number } };
  const dates = Array.from({ length: 55 }, (_, i) => new Date(Date.now() + (i + 2) * 864e5).toISOString().slice(0, 10));
  // All dates fit inside the upcoming window. Earlier hidden dates must not
  // consume the visible page's 25 slots or appear in its loaded counts.
  await admin("POST", `/groups/${group.id}/events`, {
    title: "Hidden daily series", startsLocal: `${dates[0]}T17:00`, durationMinutes: 60,
    tzid: "UTC", recurrenceRule: "FREQ=DAILY;COUNT=55", visibility: "invite_only",
  });
  const { event } = (await admin("POST", `/groups/${group.id}/events`, {
    title: "Visible daily series", startsLocal: `${dates[0]}T19:00`, durationMinutes: 60,
    tzid: "UTC", recurrenceRule: "FREQ=DAILY;COUNT=55", visibility: "public",
  })) as { event: { id: number } };

  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page, "sam");
  const groupResponse = () => page.waitForResponse((response) =>
    new URL(response.url()).pathname === `/api/v1/groups/${group.id}` && response.request().method() === "GET");
  let response = groupResponse();
  await page.goto(`/groups/${group.id}`);
  const seenIDs = new Set<number>();
  const card = page.getByRole("article", { name: "Visible daily series", exact: true });
  const loadMore = page.getByRole("button", { name: "Load more dates", exact: true });
  for (const [index, size] of [25, 25, 5].entries()) {
    const result = await response;
    expect(result.ok()).toBe(true);
    const body = await result.json() as GroupDetailResponse;
    expect(body.upcomingEvents).toHaveLength(size);
    expect(body.upcomingEvents.map((date) => date.date)).toEqual(dates.slice(index * 25, index * 25 + size));
    for (const date of body.upcomingEvents) {
      expect(date.eventId).toBe(event.id);
      expect(seenIDs.has(date.id)).toBe(false);
      seenIDs.add(date.id);
    }
    const hasMore = index < 2;
    expect(body.hasMore).toBe(hasMore);
    expect(body.nextCursor !== "").toBe(hasMore);
    await expect(card).toHaveCount(1);
    await expect(page.getByRole("article")).toHaveCount(1);
    // A truncated list says "See more dates"; "See all N" only once every date is loaded (DEC-99).
    await expect(card.getByRole("link", { name: hasMore ? "See more dates" : `See all ${seenIDs.size} dates`, exact: true })).toBeVisible();
    await expect(page.getByText("Hidden daily series", { exact: true })).toHaveCount(0);
    if (hasMore) {
      await expect(loadMore).toBeVisible();
      response = groupResponse();
      await loadMore.click();
    }
  }
  expect(seenIDs.size).toBe(55);
  await expect(loadMore).toHaveCount(0);
});

test("RSVP is a segmented control, counts come first, and Can't go never lists anyone", async ({ page, request }) => {
  const { event } = await seriesGroup(request);
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page, "sam");
  await page.goto(`/events/${event.id}`);
  const first = page.getByRole("article", { name: "Segmented night", exact: true }).first();
  const answer = first.getByRole("group", { name: "Your answer" });
  for (const name of ["Going", "Maybe", "Can't go"]) {
    const b = answer.getByRole("button", { name });
    await expect(b).toHaveAttribute("aria-pressed", "false");
    expect((await b.boundingBox())!.height).toBeGreaterThanOrEqual(44);
  }
  await expect(first.getByText("No one yet")).toBeVisible();

  await answer.getByRole("button", { name: "Maybe" }).click();
  const maybeScope = page.getByRole("dialog", { name: /How far should “Maybe” apply/ });
  const maybeJustThisDate = maybeScope.getByRole("button", { name: "Just this date" });
  await expect(maybeJustThisDate).toBeFocused();
  await maybeJustThisDate.click();
  await expect(answer.getByRole("button", { name: "Maybe" })).toHaveAttribute("aria-pressed", "true");
  // Not color alone: the chosen segment leads with a check mark.
  expect(await answer.getByRole("button", { name: "Maybe" }).evaluate((b) => getComputedStyle(b, "::before").content)).toContain("✓");
  // Full width at 390: the control spans the card's content.
  const cardBox = (await first.boundingBox())!, groupBox = (await answer.boundingBox())!;
  expect(groupBox.width).toBeGreaterThan(cardBox.width - 40);
  await expect(first.getByText("0 going · 1 maybe")).toBeVisible();
  const secondAnswer = page.getByRole("group", { name: "Your answer" }).nth(1);
  for (const name of ["Going", "Maybe", "Can't go"]) {
    await expect(secondAnswer.getByRole("button", { name })).toHaveAttribute("aria-pressed", "false");
  }
  const show = first.getByRole("button", { name: "Show who's coming" });
  await expect(show).toHaveAttribute("aria-expanded", "false");
  await show.click();
  await expect(first.getByRole("button", { name: "Hide names" })).toHaveAttribute("aria-expanded", "true");
  const attendee = first.getByRole("listitem").filter({ hasText: "Sam Member" });
  await expect(attendee).toBeVisible();
  await expect(first.getByRole("region", { name: "Maybe names" }).getByRole("heading", { name: "Maybe (1)", level: 4 })).toBeVisible();

  await answer.getByRole("button", { name: "Can't go" }).click();
  const declineScope = page.getByRole("dialog", { name: /How far should “Can't go” apply/ });
  const declineJustThisDate = declineScope.getByRole("button", { name: "Just this date" });
  await expect(declineJustThisDate).toBeFocused();
  await declineJustThisDate.click();
  await expect(answer.getByRole("button", { name: "Can't go" })).toHaveAttribute("aria-pressed", "true");
  for (const name of ["Going", "Maybe", "Can't go"]) {
    await expect(secondAnswer.getByRole("button", { name })).toHaveAttribute("aria-pressed", "false");
  }
  await expect(first.getByText("No one yet")).toBeVisible();
  await expect(first.getByText(/can.t go/i).and(first.locator(":not(button)"))).toHaveCount(0);
  await expect(first.getByText("Sam Member")).toHaveCount(0);
});

// M3.6b (DEC-133): a leader picks the reminder time in Edit series, and the
// date page tells them when it goes out.
test("a leader sets the reminder in Edit series and sees when it goes out on a date page", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  const { groups } = (await admin("GET", "/groups")) as { groups: { id: number; name: string }[] };
  const room = groups.find((g) => g.name === "Room 201 class")!;
  const lee = await apiAs(request, "lee");
  const d = new Date(Date.now() + 9 * 864e5).toISOString().slice(0, 10);
  const { event } = (await lee("POST", `/groups/${room.id}/events`, {
    title: "Reminder night", startsLocal: `${d}T19:00`, durationMinutes: 60, tzid: "America/Boise",
    location: "Room 201", recurrenceRule: "FREQ=WEEKLY;COUNT=3",
  })) as { event: { id: number } };

  await signIn(page, "lee");
  await page.goto(`/events/${event.id}`);
  await expect(page.getByRole("heading", { name: "Reminder night", level: 1 })).toBeVisible();
  await page.getByRole("button", { name: "Edit series" }).click();
  const reminder = page.getByRole("combobox", { name: "Reminder email" });
  await expect(reminder).toHaveValue("1440");
  await reminder.selectOption({ label: "2 days before" });
  await page.getByRole("button", { name: "Save series" }).click();
  await expect(page.getByRole("button", { name: "Save series" })).toHaveCount(0);
  const saved = (await lee("GET", `/events/${event.id}`)) as { event: { reminderEmailMinutes: number } };
  expect(saved.event.reminderEmailMinutes).toBe(2880);

  // Two days before 7:00 PM on the first date, on the church's clock.
  const due = new Date(`${d}T12:00:00Z`);
  due.setUTCDate(due.getUTCDate() - 2);
  const day = new Intl.DateTimeFormat("en-US", { timeZone: "UTC", weekday: "short", month: "short", day: "numeric" }).format(due);
  await page.goto(`/events/${event.id}/dates/${d}`);
  await expect(page.getByRole("heading", { name: "Reminder email", level: 2 })).toBeVisible();
  await expect(page.getByText(new RegExp(`^Reminder goes out ${day} at 7:00 PM`))).toBeVisible();
});
