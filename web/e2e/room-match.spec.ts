import { expect, test } from "@playwright/test";
import { apiAs, signIn } from "./helpers";

// M2.1e: an admin turns a typed place into a booking from the Rooms tab. The
// typed text stays, and the date shows the room once, not the room and then
// the same words again.
test("an admin books a typed place into its room and the date shows the room once", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  const { groups } = (await admin("GET", "/groups")) as { groups: { id: number; name: string }[] };
  const group = groups.find((g) => g.name === "Room 201 class")!;
  const day = new Date(Date.now() + 53 * 864e5).toISOString().slice(0, 10);
  const { event } = (await admin("POST", `/groups/${group.id}/events`, {
    title: "Typed place night", startsLocal: `${day}T11:00`, durationMinutes: 60, tzid: "America/Boise",
    location: "Fellowship Hall",
  })) as { event: { id: number } };

  await signIn(page, "grier");
  await page.goto("/admin?tab=rooms");
  await expect(page.getByRole("heading", { name: "Match old locations" })).toBeVisible();
  // Spellings that differ only in case are one place, shown in its most
  // common spelling; another spec in the shard may type "Fellowship hall".
  const row = page.locator('[data-location="Fellowship Hall" i]');
  // The room named like the place is already chosen; choosing is not booking.
  await expect(row.getByRole("combobox", { name: /^Room for “Fellowship Hall”$/i })).toHaveValue(/\d+/);
  await row.getByRole("button", { name: /^Book \d+ dates? in Fellowship Hall/i }).click();
  await expect(page.getByRole("status").filter({ hasText: /Booked Fellowship Hall · Main for \d+ dates?\./ })).toBeVisible();
  // The row may stay: dates the hall is already taken on (other specs in the
  // shard book it) stay listed for another try (M2.1e). This test's own
  // date, checked below, is free.

  await page.goto(`/events/${event.id}`);
  await expect(page.getByRole("heading", { name: "Typed place night", level: 1 })).toBeVisible();
  await expect(page.getByText("Fellowship Hall · Main", { exact: true })).toBeVisible();
  await expect(page.getByText(/Fellowship Hall · Main · Fellowship Hall/)).toHaveCount(0);
});
