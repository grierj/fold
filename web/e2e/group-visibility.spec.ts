import { expect, test } from "@playwright/test";
import { apiAs, signIn, fromMore } from "./helpers";

function futureDate(days: number): string {
  return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
}

// DEC-48: making a group more private shows the leader its events first.
// Inherited events tighten (registrants who lose access are cancelled and
// told); a leader-opened event needs a keep-or-tighten choice.
test("making a group invite-only reviews its events before saving", async ({ page, request }) => {
  test.setTimeout(30_000);
  const admin = await apiAs(request, "grier");
  const { group } = (await admin("POST", "/groups", {
    name: "Supper club", description: "", tags: [], imageUrl: "", location: "Fellowship hall",
    visibility: "private", memberLimit: null,
  })) as { group: { id: number } };
  const date = futureDate(20);
  const make = async (title: string) =>
    ((await admin("POST", `/groups/${group.id}/events`, {
      title, startsLocal: `${date}T18:00`, durationMinutes: 60, tzid: "America/Boise", location: "", recurrenceRule: "",
    })) as { event: { id: number } }).event.id;
  const soup = await make("Soup night");
  const bake = await make("Bake sale");
  await admin("PUT", `/events/${bake}/visibility`, { visibility: "public", reason: "neighbors welcome" });
  // apiAs signs this request context in, so sign Quinn in last and Grier again.
  await (await apiAs(request, "quinn"))("PUT", `/events/${soup}/registrations`, { status: "going", date });
  const grier = await apiAs(request, "grier");

  await signIn(page, "grier");
  await page.goto(`/groups/${group.id}`);
  await fromMore(page, "Edit group", "More group actions");
  await page.getByRole("radio", { name: /^Invite only/ }).check();
  await page.getByRole("button", { name: "Save group" }).click();

  const review = page.getByRole("dialog");
  await expect(review.getByText("Making the group invite-only changes some events")).toBeVisible();
  await expect(review.getByRole("list", { name: "Events that will match the group" })).toContainText("Soup night");
  await expect(review).toContainText("1 person registered will be cancelled and notified");
  const confirm = review.getByRole("button", { name: "Make the group invite-only" });
  await expect(confirm).toBeDisabled();
  // Nothing is saved while the leader decides.
  expect(((await grier("GET", `/groups/${group.id}`)) as { group: { visibility: string } }).group.visibility).toBe("private");

  await review.getByTestId(`opened-${bake}`).getByRole("radio", { name: /Keep it public/ }).check();
  await confirm.click();
  await expect(review).toHaveCount(0);

  const vis = async (id: number) => ((await grier("GET", `/events/${id}`)) as { event: { visibility: string } }).event.visibility;
  expect(await vis(soup)).toBe("invite_only");
  expect(await vis(bake)).toBe("public");

  await page.context().clearCookies();
  await signIn(page, "quinn");
  await page.getByRole("button", { name: "Notifications" }).click();
  await expect(page.getByText("Registration cancelled: Soup night")).toBeVisible();
  await expect(page.getByText("Supper club")).toHaveCount(0);
  await expect(page.getByText("Fellowship hall")).toHaveCount(0);
});
