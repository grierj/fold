import { expect, test } from "@playwright/test";
import { apiAs, pickRoom, signIn } from "./helpers";

// M2.1d: with approval on, a leader's room booking waits for an admin, the
// admin confirms it from the Rooms tab, and the leader's date shows the room.
// The setting is shared by every spec in the shard, so it is turned off again
// whatever happens.
test("a leader's room waits for an admin, who confirms it", async ({ page, request }) => {
  try {
    const admin = await apiAs(request, "grier");
    await admin("PUT", "/admin/settings/room-approval", { enabled: true });
    const { groups } = (await admin("GET", "/groups")) as { groups: { id: number; name: string }[] };
    const group = groups.find((g) => g.name === "Room 201 class")!;
    const lee = await apiAs(request, "lee");
    const day = new Date(Date.now() + 47 * 864e5).toISOString().slice(0, 10);
    const { event } = (await lee("POST", `/groups/${group.id}/events`, {
      title: "Approval night", startsLocal: `${day}T14:00`, durationMinutes: 60, tzid: "America/Boise",
      location: "south door",
    })) as { event: { id: number } };

    // The leader asks for the hall and is told it is waiting.
    await signIn(page, "lee");
    await page.goto(`/events/${event.id}`);
    await expect(page.getByRole("heading", { name: "Approval night", level: 1 })).toBeVisible();
    await page.getByRole("button", { name: "Edit event" }).click();
    await pickRoom(page.getByRole("combobox", { name: /^Room/ }), "Fellowship Hall · Main (80 seats)");
    await expect(page.getByRole("status").filter({ hasText: "Waiting for an admin to approve Fellowship Hall." })).toBeVisible();
    // Nothing is held yet: the place is still the typed location.
    await expect(page.getByText("south door", { exact: true })).toBeVisible();

    // The admin sees it under Needs a room and approves it.
    await signIn(page, "grier");
    await page.goto("/admin?tab=rooms");
    await expect(page.getByRole("heading", { name: "Needs a room" })).toBeVisible();
    const row = page.getByRole("listitem").filter({ has: page.getByRole("heading", { name: "Approval night" }) });
    await expect(row.getByText("Fellowship Hall · Main", { exact: true })).toBeVisible();
    await row.getByRole("button", { name: "Approve for Approval night" }).click();
    await expect(page.getByRole("status").filter({ hasText: "Approved Fellowship Hall · Main for Approval night: 1 date." })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Approval night" })).toHaveCount(0);

    // The leader's date now shows the room first, then the typed location.
    await signIn(page, "lee");
    await page.goto(`/events/${event.id}`);
    await expect(page.getByText("Fellowship Hall · Main · south door", { exact: true })).toBeVisible();
  } finally {
    const admin = await apiAs(request, "grier");
    await admin("PUT", "/admin/settings/room-approval", { enabled: false });
  }
});
