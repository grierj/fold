import { expect, test } from "@playwright/test";
import { apiAs, signIn } from "./helpers";

function futureDate(days: number): string {
  return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
}

// Making one event more private previews the cancellations and saves only
// after the leader confirms, the same as a group (DEC-48).
test("making one event invite-only previews and confirms", async ({ page, request }) => {
  test.setTimeout(30_000);
  const admin = await apiAs(request, "grier");
  const { group } = (await admin("POST", "/groups", {
    name: "Garden club", description: "", tags: [], imageUrl: "", location: "Back lot",
    visibility: "public", memberLimit: null,
  })) as { group: { id: number } };
  const date = futureDate(21);
  const ev = ((await admin("POST", `/groups/${group.id}/events`, {
    title: "Seed swap", startsLocal: `${date}T10:00`, durationMinutes: 60, tzid: "America/Boise", location: "", recurrenceRule: "",
  })) as { event: { id: number } }).event.id;
  await (await apiAs(request, "quinn"))("PUT", `/events/${ev}/registrations`, { status: "going", date });
  const grier = await apiAs(request, "grier");

  await signIn(page, "grier");
  await page.goto(`/groups/${group.id}`);
  await page.getByLabel("Event visibility").selectOption("invite_only");
  await page.getByRole("button", { name: "Save visibility" }).click();
  const review = page.getByRole("dialog");
  await expect(review.getByText("Making this event invite-only")).toBeVisible();
  await expect(review).toContainText("1 person registered will be cancelled and notified");
  const vis = async () => ((await grier("GET", `/events/${ev}`)) as { event: { visibility: string } }).event.visibility;
  expect(await vis()).toBe("public");

  await review.getByRole("button", { name: "Make this event invite-only" }).click();
  await expect(review).toHaveCount(0);
  expect(await vis()).toBe("invite_only");

  await page.context().clearCookies();
  await signIn(page, "quinn");
  await page.getByRole("button", { name: "Notifications" }).click();
  await expect(page.getByText("Registration cancelled: Seed swap")).toBeVisible();
});
