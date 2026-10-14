import { expect, test } from "@playwright/test";
import { apiAs, email, signIn, signOut } from "./helpers";

test.use({ locale: "en-US", timezoneId: "America/New_York" });

function futureDate(days: number): string {
  return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
}

test("an admin schedules a church-wide event from the browser zone", async ({ page }) => {
  await signIn(page, "grier");
  await page.goto("/events/new");
  await expect(page.getByLabel("Time zone")).toHaveValue("America/New_York");

  await page.getByLabel("Title").fill("Church potluck");
  await page.getByLabel("Description").fill("Bring a side dish.");
  await page.getByLabel("Date").fill(futureDate(18));
  await page.getByLabel("Start time").fill("18:00");
  await page.getByRole("button", { name: "Schedule event" }).click();

  await expect(page).toHaveURL(/\/events\/\d+$/);
  await expect(page.getByRole("heading", { name: "Church potluck" })).toBeVisible();
  await expect(page.getByText("Bring a side dish.")).toBeVisible();
  await expect(page.getByText("Hosted by Fold e2e")).toBeVisible();

  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Coming up" })).toBeVisible();
  await expect(page.getByRole("link", { name: /Church potluck/ })).toBeVisible();
  await expect(page.getByRole("link", { name: /Church potluck/ })).toContainText("Hosted by Fold e2e");
});

test("opening an invite-only group's event hides the group and the names", async ({ page, request }) => {
  test.setTimeout(30_000);
  const admin = await apiAs(request, "grier");
  const { users } = (await admin("GET", "/admin/users")) as { users: { id: number; email: string }[] };
  const sam = users.find((u) => u.email === email("sam"));
  expect(sam).toBeTruthy();

  const { group } = (await admin("POST", "/groups", {
    name: "Hidden Care",
    description: "Not for visitors.",
    tags: [],
    imageUrl: "",
    location: "",
    visibility: "invite_only",
    memberLimit: null,
  })) as { group: { id: number } };
  await admin("POST", `/admin/groups/${group.id}/members`, { userId: sam!.id, role: "member" });

  const date = futureDate(21);
  const { event } = (await admin("POST", `/groups/${group.id}/events`, {
    title: "Quiet supper",
    startsLocal: `${date}T18:00`,
    durationMinutes: 60,
    tzid: "America/Boise",
    location: "",
    recurrenceRule: "",
  })) as { event: { id: number } };
  await (await apiAs(request, "sam"))("PUT", `/events/${event.id}/registrations`, { status: "going", date });

  await signIn(page, "grier");
  await page.goto(`/groups/${group.id}`);
  await page.getByLabel("Event visibility").selectOption("public");
  await expect(
    page.getByText("Only leaders will see who signed up, so members of Hidden Care aren't revealed.")
  ).toBeVisible();
  const saved = page.waitForResponse((r) => r.url().includes("/visibility") && r.request().method() === "PUT");
  await page.getByRole("button", { name: "Save visibility" }).click();
  expect((await saved).ok()).toBe(true);
  await expect(page.getByLabel("Event visibility")).toHaveValue("public");

  await signOut(page);

  await page.goto(`/events/${event.id}`);
  await expect(page.getByRole("heading", { name: "Quiet supper" })).toBeVisible();
  await expect(page.getByText("Hosted by Fold e2e")).toBeVisible();
  await expect(page.getByText("1 going")).toBeVisible();
  await expect(page.getByText("Hidden Care")).toHaveCount(0);
  await expect(page.getByText("Sam Member")).toHaveCount(0);
  await expect(page.getByRole("link", { name: "Hidden Care" })).toHaveCount(0);

  await page.goto(`/groups/${group.id}`);
  await expect(page.getByRole("heading", { name: "Group not found" })).toBeVisible();
});

test("an admin edits one date, cancels a date, and deletes a church-wide event", async ({ page, request }) => {
  page.on("dialog", (d) => d.accept());
  const admin = await apiAs(request, "grier");
  const mk = async (title: string, days: number) =>
    ((await admin("POST", "/events", {
      title,
      startsLocal: `${futureDate(days)}T19:00`,
      durationMinutes: 60,
      tzid: "America/Boise",
      location: "Hall",
      visibility: "public",
      hosts: [],
      recurrenceRule: "",
    })) as { event: { id: number } }).event;

  // Per-date edit: only that date changes.
  const edited = await mk("Date night", 26);
  await signIn(page, "grier");
  await page.goto(`/events/${edited.id}`);
  await page.getByRole("button", { name: "Edit this date" }).click();
  await page.getByLabel("Location", { exact: true }).fill("Gym");
  await page.getByRole("button", { name: "Save this date" }).click();
  await expect(page.getByText(/Gym/)).toBeVisible();
  await expect(page.getByText(/edited on its own/)).toBeVisible();

  // Cancelling the only date leaves no upcoming dates.
  const cancelled = await mk("Cancel night", 27);
  await page.goto(`/events/${cancelled.id}`);
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(page.getByText("No upcoming dates.")).toBeVisible();

  // Members without manage rights see none of these controls.
  await signOut(page);
  await signIn(page, "sam");
  await page.goto(`/events/${edited.id}`);
  await expect(page.getByRole("button", { name: "Edit this date" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Delete event" })).toHaveCount(0);
  await signOut(page);

  // Delete removes the event entirely.
  await signIn(page, "grier");
  await page.goto(`/events/${edited.id}`);
  await page.getByRole("button", { name: "Delete event" }).click();
  await expect(page).toHaveURL(/\/$/);
  await page.goto(`/events/${edited.id}`);
  await expect(page.getByRole("heading", { name: "Event not found" })).toBeVisible();
});

test("a non-admin host can edit the church-wide event they host", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  const date = futureDate(24);
  const { event } = (await admin("POST", "/events", {
    title: "Host night",
    startsLocal: `${date}T19:00`,
    durationMinutes: 60,
    tzid: "America/Boise",
    location: "Hall",
    visibility: "public",
    hosts: [email("lee")],
    recurrenceRule: "",
  })) as { event: { id: number } };

  await signIn(page, "lee");
  await page.goto(`/events/${event.id}`);
  await expect(page.getByText("grier@example.com")).toHaveCount(0);
  await page.getByRole("button", { name: "Edit series" }).click();
  await page.getByLabel("Title").fill("Host night revised");
  await page.getByRole("button", { name: "Save series" }).click();
  await expect(page.getByRole("heading", { name: "Host night revised" })).toBeVisible();

  await page.goto("/events/new");
  await expect(page.getByText("Only an admin can schedule a church-wide event.")).toBeVisible();
});
