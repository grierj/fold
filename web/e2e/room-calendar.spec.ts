import { expect, test } from "@playwright/test";
import { apiAs, signIn } from "./helpers";

// M2.2a: a member opens Rooms from the calendar and sees each room's
// bookings; a booking for a date they can't see is a grey "Reserved" block
// with no title or link.
test("a member sees the room calendar, with a hidden date as Reserved", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  const { groups } = (await admin("GET", "/groups")) as { groups: { id: number; name: string }[] };
  const group = groups.find((g) => g.name === "Room 201 class")!;
  const { rooms } = (await admin("GET", "/admin/rooms")) as { rooms: { id: number; name: string }[] };
  const hall = rooms.find((r) => r.name === "Fellowship Hall")!;
  const day = new Date(Date.now() + 61 * 864e5).toISOString().slice(0, 10);

  const lee = await apiAs(request, "lee");
  const { event } = (await lee("POST", `/groups/${group.id}/events`, {
    title: "Room calendar night", startsLocal: `${day}T19:00`, durationMinutes: 60, tzid: "America/Boise",
  })) as { event: { id: number } };
  await lee("PUT", `/events/${event.id}/dates/${day}/room`, { roomId: hall.id });

  // An invite-only group Sam isn't in holds the hall that morning.
  const { group: hidden } = (await admin("POST", "/groups", { name: "Room calendar hidden group", visibility: "invite_only", tags: [] })) as {
    group: { id: number };
  };
  const { event: secret } = (await admin("POST", `/groups/${hidden.id}/events`, {
    title: "Hidden room meeting", startsLocal: `${day}T09:00`, durationMinutes: 60, tzid: "America/Boise",
  })) as { event: { id: number } };
  await admin("PUT", `/events/${secret.id}/dates/${day}/room`, { roomId: hall.id });

  await signIn(page, "sam");
  await page.goto(`/calendar?view=week&date=${day}`);
  await page.getByRole("navigation", { name: "Calendar view" }).getByRole("link", { name: "Rooms" }).click();
  await expect(page).toHaveURL(new RegExp(`/calendar/rooms\\?date=${day}$`));

  const column = page.getByRole("region", { name: /Fellowship Hall/ });
  const booking = column.getByRole("link", { name: /Room calendar night, Room 201 class/ });
  await expect(booking).toBeVisible();
  await expect(column.getByText("Reserved", { exact: true })).toBeVisible();
  await expect(page.getByText("Hidden room meeting")).toHaveCount(0);

  await booking.click();
  await expect(page.getByRole("heading", { name: "Room calendar night", level: 1 })).toBeVisible();
});
