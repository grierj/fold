import { expect, test } from "@playwright/test";
import { apiAs, signIn } from "./helpers";

const day = (n: number) => new Date(Date.now() + n * 864e5).toISOString().slice(0, 10);

test("a series answer applies, reports its result, can be undone, and confirms bulk Can't go", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  const { event } = await admin("POST", "/events", {
    title: "Series answer browser check", startsLocal: `${day(5)}T18:00`, durationMinutes: 60,
    tzid: "UTC", location: "Hall", recurrenceRule: "FREQ=WEEKLY;COUNT=3", visibility: "public",
  }) as { event: { id: number } };

  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page, "sam");
  await page.goto(`/events/${event.id}`);
  const answers = page.getByRole("group", { name: "Your answer" });
  await answers.first().getByRole("button", { name: "Going" }).click();
  const dialog = page.getByRole("dialog", { name: /How far should/ });
  const firstOption = dialog.getByRole("button", { name: "Just this date" });
  await expect(firstOption).toBeFocused();
  expect((await firstOption.boundingBox())?.height).toBeGreaterThanOrEqual(44);
  const dialogBox = await dialog.boundingBox();
  expect(dialogBox && Math.abs(dialogBox.y + dialogBox.height - 844)).toBeLessThanOrEqual(1);
  await dialog.getByRole("button", { name: "This and following dates" }).click();
  await expect(page.getByRole("status").filter({ hasText: /Going to 3 dates/ })).toBeVisible();
  await page.getByRole("button", { name: "Undo" }).click();
  await expect(page.getByRole("status").filter({ hasText: /Undo:/ })).toBeVisible();

  page.once("dialog", async (confirm) => {
    expect(confirm.message()).toContain("removed from your calendar and personal feed");
    await confirm.dismiss();
  });
  await page.getByRole("button", { name: "Can't go to any" }).first().click();
  await expect(page.getByRole("status").filter({ hasText: /Undo:/ })).toBeVisible();
});
