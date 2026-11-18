import * as schema from "valibot";
import { expect, test } from "@playwright/test";
import { apiAs, signIn } from "./helpers";

// M1.5: capacity and waitlist. A leader sets capacity 1 with the waitlist
// on. One member goes, a second sees position 1, the first cancels, and
// the second is going with one bell item.
test("the waitlist arc: capacity 1, position 1, cancel, promoted", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");

  const { group } = schema.parse(schema.looseObject({ group: schema.looseObject({ id: schema.number() }) }), (await admin("POST", "/groups", {
    name: "Waitlist club", description: "", tags: [], imageUrl: "", location: "Hall",
    visibility: "public", memberLimit: null,
  })));

  const { users } = schema.parse(schema.looseObject({ users: schema.array(schema.looseObject({ id: schema.number(), email: schema.string() })) }), (await admin("GET", "/admin/users")));
  const id = (name: string) => users.find((u) => u.email === `${name}@example.com`)!.id;

  for (const name of ["sam", "lee"]) {
    await admin("POST", `/admin/groups/${group.id}/members`, { userId: id(name), role: "member" });
  }

  const when = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000);
  const ymd = when.toISOString().slice(0, 10);

  const { event } = schema.parse(schema.looseObject({ event: schema.looseObject({ id: schema.number() }) }), (await admin("POST", `/groups/${group.id}/events`, {
    title: "Tiny supper",
    startsLocal: `${ymd}T19:00`,
    durationMinutes: 60,
    tzid: "UTC",
    capacity: 1,
    waitlistEnabled: true,
  })));

  const url = `/events/${event.id}`;

  // Sam takes the one seat.
  await signIn(page, "sam");
  await page.goto(url);
  await page.getByRole("button", { name: "Going" }).click();
  await expect(page.getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "true");

  // Lee sees position 1 on the waitlist.
  await signIn(page, "lee");
  await page.goto(url);
  await page.getByRole("button", { name: "Going" }).click();
  await expect(page.getByText(/position 1/)).toBeVisible();

  // Sam cancels; Lee is promoted.
  await signIn(page, "sam");
  await page.goto(url);
  await page.getByRole("button", { name: "Can't go" }).click();
  await expect(page.getByRole("button", { name: "Can't go" })).toHaveAttribute("aria-pressed", "true");

  await signIn(page, "lee");
  await page.goto(url);
  await expect(page.getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "true");
  const bell = page.getByRole("button", { name: /Alerts/ });
  await expect(bell).toHaveAccessibleName(/unread/);
  await bell.click();
  const panel = page.locator("#notif-panel");
  await expect(panel.getByText("You're in: Tiny supper", { exact: true })).toBeVisible();
});
