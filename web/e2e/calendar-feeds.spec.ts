import { expect, test } from "@playwright/test";
import { apiAs, signIn } from "./helpers";

// M1.7: a personal calendar link from the profile page serves the events
// of the groups Sam belongs to; remaking the link kills the old one; the
// public feed never carries an invite-only group's events.
test("personal calendar link serves my events and remaking it kills the old one", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  const soon = new Date(Date.now() + 7 * 24 * 60 * 60 * 1000);
  const pad = (n: number) => String(n).padStart(2, "0");
  const startsLocal = `${soon.getFullYear()}-${pad(soon.getMonth() + 1)}-${pad(soon.getDate())}T19:00`;

  const { groups } = await admin("GET", "/groups");
  const room201 = (groups as { id: number; name: string }[]).find((g) => g.name === "Room 201 class");
  expect(room201, "seed group Room 201 class").toBeTruthy();
  await admin("POST", `/groups/${room201!.id}/events`, {
    title: "Feed Rehearsal Night", startsLocal, durationMinutes: 60, tzid: "America/Boise",
  });

  const { group: hidden } = await admin("POST", "/groups", {
    name: "Feed Hidden Circle", description: "", tags: [], imageUrl: "", location: "",
    visibility: "invite_only", memberLimit: null,
  });
  await admin("POST", `/groups/${hidden.id}/events`, {
    title: "Feed Secret Night", startsLocal, durationMinutes: 60, tzid: "America/Boise",
  });

  await signIn(page, "sam");
  await page.goto("/profile/calendar");
  await expect(page.getByRole("heading", { name: "Calendar feed", level: 1 })).toBeVisible();
  await page.getByRole("button", { name: "Create calendar link" }).click();
  const linkField = page.getByLabel("Your calendar link");
  await expect(linkField).toBeVisible();
  const firstLink = await linkField.inputValue();
  expect(firstLink).toContain("/cal/u/");
  const firstPath = new URL(firstLink).pathname;

  const mine = await request.get(firstPath);
  expect(mine.ok()).toBe(true);
  expect(mine.headers()["content-type"]).toContain("text/calendar");
  expect(await mine.text()).toContain("Feed Rehearsal Night");

  page.once("dialog", (d) => void d.accept());
  await page.getByRole("button", { name: "Make a new link" }).click();
  await expect(linkField).not.toHaveValue(firstLink);
  const secondPath = new URL(await linkField.inputValue()).pathname;

  const old = await request.get(firstPath);
  expect(old.status()).toBe(404);
  const fresh = await request.get(secondPath);
  expect(fresh.ok()).toBe(true);
  expect(await fresh.text()).toContain("Feed Rehearsal Night");

  const pub = await request.get("/cal/public.ics");
  expect(pub.ok()).toBe(true);
  const pubBody = await pub.text();
  expect(pubBody).toContain("Feed Rehearsal Night");
  expect(pubBody).not.toContain("Feed Secret Night");
  expect(pubBody).not.toContain("Feed Hidden Circle");
});
