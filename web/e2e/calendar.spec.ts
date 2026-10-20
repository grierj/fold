import { expect, test } from "@playwright/test";
import { apiAs, signIn, signOut } from "./helpers";

// The calendar (M1.6): a smoke test per view, Just mine, invite-only
// privacy, and keyboard order.
const day = (n: number) => new Date(Date.now() + n * 864e5).toISOString().slice(0, 10);

test("the calendar shows what sam may see, in all three views", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  const { group: secret } = (await admin("POST", "/groups", {
    name: "Calendar secret", description: "", tags: [], imageUrl: "", location: "", visibility: "invite_only", memberLimit: null,
  })) as { group: { id: number } };
  await admin("POST", `/groups/${secret.id}/events`, {
    title: "Secret supper", startsLocal: `${day(2)}T18:00`, durationMinutes: 60, tzid: "America/Boise", location: "", recurrenceRule: "",
  });
  await admin("POST", "/events", {
    title: "Calendar picnic", startsLocal: `${day(2)}T12:00`, durationMinutes: 60, tzid: "America/Boise", location: "Park",
    recurrenceRule: "", visibility: "public",
  });

  await signIn(page, "sam");
  await page.goto("/calendar");
  await expect(page.getByRole("heading", { name: "Calendar", level: 1 })).toBeVisible();
  await expect(page.getByRole("link", { name: /Calendar picnic/ }).first()).toBeVisible();
  await expect(page.getByText("Secret supper")).toHaveCount(0);

  // Just mine drops the church-wide picnic sam hasn't answered.
  await page.getByRole("button", { name: "Just mine" }).click();
  await expect(page).toHaveURL(/mine=1/);
  await expect(page.getByRole("link", { name: /Calendar picnic/ })).toHaveCount(0);
  await page.getByRole("button", { name: "Just mine" }).click();

  await page.getByRole("link", { name: "Week", exact: true }).click();
  await expect(page).toHaveURL(/view=week/);
  await expect(page.getByRole("heading", { level: 2 }).filter({ hasText: /\d{4}/ })).toBeVisible();
  await page.getByRole("button", { name: "Next week" }).click();
  await page.getByRole("button", { name: "Previous week" }).click();
  await page.getByRole("link", { name: "Month", exact: true }).click();
  await expect(page.getByRole("table")).toBeVisible();
  await expect(page.getByText("Secret supper")).toHaveCount(0);

  // Keyboard: the view switch, then period nav, then the chip and picker.
  await page.getByRole("link", { name: "Agenda", exact: true }).focus();
  const order: string[] = [];
  for (let i = 0; i < 7; i++) {
    await page.keyboard.press("Tab");
    order.push(await page.evaluate(() => document.activeElement?.getAttribute("aria-label") || document.activeElement?.textContent?.trim() || ""));
  }
  expect(order.slice(0, 6)).toEqual(["Week", "Month", "Previous month", "Next month", "Today", "Just mine"]);
  await signOut(page);
});

test("signed out, the calendar shows public events and says so", async ({ page }) => {
  await page.context().clearCookies();
  await page.goto("/calendar");
  await expect(page.getByText("Showing public events.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Just mine" })).toHaveCount(0);
});
