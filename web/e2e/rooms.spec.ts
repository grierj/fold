import { expect, test } from "@playwright/test";
import { apiAs, signIn } from "./helpers";

// M2.1b: a leader books a room for one date from the event editor, the room
// becomes the date's place, and releasing it brings the typed location back.
test("a leader books the Fellowship Hall for a date, sees it as the place, and releases it", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  const { groups } = (await admin("GET", "/groups")) as { groups: { id: number; name: string }[] };
  const group = groups.find((g) => g.name === "Room 201 class")!;
  const lee = await apiAs(request, "lee");
  const day = new Date(Date.now() + 40 * 864e5).toISOString().slice(0, 10);
  const { event } = (await lee("POST", `/groups/${group.id}/events`, {
    title: "Room booking night", startsLocal: `${day}T19:00`, durationMinutes: 60, tzid: "America/Boise",
    location: "north door",
  })) as { event: { id: number } };

  await signIn(page, "lee");
  await page.goto(`/events/${event.id}`);
  await expect(page.getByRole("heading", { name: "Room booking night", level: 1 })).toBeVisible();
  await expect(page.getByText("north door", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Edit event" }).click();
  const room = page.getByRole("combobox", { name: /^Room/ });
  await expect(room.getByRole("option", { name: "Fellowship Hall · Main (80 seats)" })).toBeAttached();
  await room.selectOption({ label: "Fellowship Hall · Main (80 seats)" });
  await expect(page.getByRole("status").filter({ hasText: "Booked Fellowship Hall." })).toBeVisible();
  // Booking needs no Save: the page behind already shows the room first, then the typed location.
  await expect(page.getByText("Fellowship Hall · Main · north door", { exact: true })).toBeVisible();

  await room.selectOption({ label: "No room" });
  await expect(page.getByRole("status").filter({ hasText: "Room released." })).toBeVisible();
  await expect(page.getByText("north door", { exact: true })).toBeVisible();
});

// M2.1c: a leader books one room for every date of a weekly event from Edit
// series, and each date's card shows it as the place.
test("a leader books the Fellowship Hall for every date of a weekly event", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  const { groups } = (await admin("GET", "/groups")) as { groups: { id: number; name: string }[] };
  const group = groups.find((g) => g.name === "Room 201 class")!;
  const lee = await apiAs(request, "lee");
  const day = new Date(Date.now() + 41 * 864e5).toISOString().slice(0, 10);
  const weekday = ["SU", "MO", "TU", "WE", "TH", "FR", "SA"][new Date(`${day}T12:00:00Z`).getUTCDay()];
  const { event } = (await lee("POST", `/groups/${group.id}/events`, {
    title: "Weekly room night", startsLocal: `${day}T08:00`, durationMinutes: 30, tzid: "America/Boise",
    recurrenceRule: `FREQ=WEEKLY;BYDAY=${weekday}`,
  })) as { event: { id: number } };

  await signIn(page, "lee");
  await page.goto(`/events/${event.id}`);
  await expect(page.getByRole("heading", { name: "Weekly room night", level: 1 })).toBeVisible();
  await expect(page.getByRole("article").first()).not.toContainText("Fellowship Hall");

  await page.getByRole("button", { name: "Edit series" }).click();
  const room = page.getByRole("combobox", { name: /^Room for every date/ });
  await room.selectOption({ label: "Fellowship Hall · Main (80 seats)" });
  // Choosing books nothing: the button does.
  await expect(page.getByRole("article").first()).not.toContainText("Fellowship Hall");
  await page.getByRole("button", { name: "Book for every date" }).click();
  await expect(page.getByRole("status").filter({ hasText: /Booked for \d+ dates\./ })).toBeVisible();
  // Booking needs no Save: the date cards behind already show the room.
  await expect(page.getByRole("article").first()).toContainText("Fellowship Hall · Main");

  await room.selectOption({ label: "No series room" });
  await page.getByRole("button", { name: "Stop the series room" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Series room released." })).toBeVisible();
  await expect(page.getByRole("article").first()).not.toContainText("Fellowship Hall");
});

// M2.1c: a series room that is busy on one date books the other dates, lists
// the busy one as needing a room, and "Pick a room" gives that date another.
test("a series room busy on one date lists it, and Pick a room books another room for it", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  await admin("POST", "/admin/rooms", { name: "Library", capacity: 20 });
  const { groups } = (await admin("GET", "/groups")) as { groups: { id: number; name: string }[] };
  const group = groups.find((g) => g.name === "Room 201 class")!;
  const { rooms } = (await admin("GET", "/admin/rooms")) as { rooms: { id: number; name: string }[] };
  const hall = rooms.find((r) => r.name === "Fellowship Hall")!;
  const lee = await apiAs(request, "lee");
  const day = new Date(Date.now() + 43 * 864e5).toISOString().slice(0, 10);
  const weekday = ["SU", "MO", "TU", "WE", "TH", "FR", "SA"][new Date(`${day}T12:00:00Z`).getUTCDay()];
  // Someone else's one-date event holds the hall on the series' first date.
  const { event: other } = (await lee("POST", `/groups/${group.id}/events`, {
    title: "Hall holder", startsLocal: `${day}T09:00`, durationMinutes: 30, tzid: "America/Boise",
  })) as { event: { id: number } };
  await lee("PUT", `/events/${other.id}/dates/${day}/room`, { roomId: hall.id });
  const { event } = (await lee("POST", `/groups/${group.id}/events`, {
    title: "Busy first date", startsLocal: `${day}T09:00`, durationMinutes: 30, tzid: "America/Boise",
    recurrenceRule: `FREQ=WEEKLY;BYDAY=${weekday}`,
  })) as { event: { id: number } };

  await signIn(page, "lee");
  await page.goto(`/events/${event.id}`);
  await expect(page.getByRole("heading", { name: "Busy first date", level: 1 })).toBeVisible();
  await page.getByRole("button", { name: "Edit series" }).click();
  const room = page.getByRole("combobox", { name: /^Room for every date/ });
  const hallOption = room.locator("option", { hasText: /^Fellowship Hall · Main \(80 seats\) — in use on 1 of \d+ dates$/ });
  await room.selectOption((await hallOption.getAttribute("value"))!);
  await expect(page.getByRole("status").filter({ hasText: "will be listed under" })).toBeVisible();
  await page.getByRole("button", { name: "Book for every date" }).click();
  await expect(page.getByRole("status").filter({ hasText: "1 date is busy" })).toBeVisible();

  const list = page.getByRole("region", { name: /^Dates that need a room \(1\)/ });
  await expect(list).toBeVisible();
  await list.getByRole("button", { name: /^Pick a room/ }).click();
  const dateRoom = page.getByRole("combobox", { name: /^Room \(optional\)/ });
  await expect(dateRoom).toBeFocused();
  await dateRoom.selectOption({ label: "Library (20 seats)" });
  await expect(page.getByRole("status").filter({ hasText: "Booked Library." })).toBeVisible();
  await expect(page.getByRole("heading", { name: /Dates that need a room/ })).toHaveCount(0);
});
