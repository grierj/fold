import { expect, test } from "@playwright/test";
import { apiAs, signIn, signOut } from "./helpers";

// M1.5: capacity and waitlist. A leader sets capacity 1 with the waitlist
// on. One member goes, a second sees position 1, the first cancels, and
// the second is going with one bell item.
test("the waitlist arc: capacity 1, position 1, cancel, promoted", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  const { group } = (await admin("POST", "/groups", {
    name: "Waitlist club", description: "", tags: [], imageUrl: "", location: "Hall",
    visibility: "public", memberLimit: null,
  })) as { group: { id: number } };
  const { users } = (await admin("GET", "/admin/users")) as { users: { id: number; email: string }[] };
  const id = (name: string) => users.find((u) => u.email === `${name}@example.com`)!.id;
  for (const name of ["sam", "lee"]) {
    await admin("POST", `/admin/groups/${group.id}/members`, { userId: id(name), role: "member" });
  }
  const when = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000);
  const ymd = when.toISOString().slice(0, 10);
  const { event } = (await admin("POST", `/groups/${group.id}/events`, {
    title: "Tiny supper",
    startsLocal: `${ymd}T19:00`,
    durationMinutes: 60,
    tzid: "UTC",
    capacity: 1,
    waitlistEnabled: true,
  })) as { event: { id: number } };
  const url = `/events/${event.id}`;

  // Sam takes the one seat.
  await signIn(page, "sam");
  await page.goto(url);
  await page.getByRole("button", { name: "Going" }).click();
  await expect(page.getByRole("button", { name: "Going" })).toHaveClass(/bg-primary/);

  // Lee sees position 1 on the waitlist.
  await signOut(page);
  await signIn(page, "lee");
  await page.goto(url);
  await page.getByRole("button", { name: "Going" }).click();
  await expect(page.getByText(/position 1/)).toBeVisible();

  // Sam cancels; Lee is promoted.
  await signOut(page);
  await signIn(page, "sam");
  await page.goto(url);
  await page.getByRole("button", { name: "Can't go" }).click();
  await expect(page.getByRole("button", { name: "Can't go" })).toHaveClass(/bg-primary/);

  await signOut(page);
  await signIn(page, "lee");
  await page.goto(url);
  await expect(page.getByRole("button", { name: "Going" })).toHaveClass(/bg-primary/);
  const bell = page.getByRole("button", { name: /^Notifications/ });
  await expect(bell).toHaveAccessibleName(/unread/);
  await bell.click();
  const panel = page.locator("#notif-panel");
  await expect(panel.getByText(/You're in/)).toBeVisible();
});
