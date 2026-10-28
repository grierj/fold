import { expect, test } from "@playwright/test";
import { apiAs } from "./helpers";

// M1.4 in the browser: the guest door on a public event, its age gate,
// and the confirm and manage pages faced with a bad link. The e2e
// server runs without a mail sender (prod mode, like the password
// reset and account request specs), so the emailed token half of the
// flow is proven by the Go handler tests against the devoutbox.

test("guest form: age gate keeps the typed fields, then the uniform thanks", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  const { groups } = await admin("GET", "/groups");
  const group = (groups as { id: number; name: string }[]).find((g) => g.name === "Room 201 class");
  expect(group, "seed group").toBeTruthy();
  const when = new Date(Date.now() + 21 * 24 * 60 * 60 * 1000);
  const { event } = await admin("POST", `/groups/${group!.id}/events`, {
    title: "Guest welcome night",
    startsLocal: `${when.toISOString().slice(0, 10)}T19:00`,
    durationMinutes: 60,
    tzid: "America/Boise",
  });

  await page.goto(`/events/${event.id}`);
  await expect(page.getByRole("heading", { name: "Guest welcome night", level: 1 })).toBeVisible();
  await page.getByRole("button", { name: "Register as a guest" }).click();
  await page.getByLabel("Full name").fill("Browser Gail");
  await page.getByLabel("Email").fill("gail-e2e@example.com");

  // Without the 18-or-older tick, the guardian message shows and the
  // typed values stay put.
  await page.getByRole("button", { name: "Email me a confirmation link" }).click();
  await expect(page.getByText(/parent or guardian to register you/i)).toBeVisible();
  await expect(page.getByLabel("Full name")).toHaveValue("Browser Gail");
  await expect(page.getByLabel("Email")).toHaveValue("gail-e2e@example.com");

  // Ticking it sends the verification email (which this server cannot
  // deliver) and answers like every other public form.
  await page.getByLabel(/I am 18 or older/).check();
  await page.getByRole("button", { name: "Email me a confirmation link" }).click();
  await expect(page.getByText(/check your email/i)).toBeVisible();
  await expect(page.getByText(/confirmed when you open the link/i)).toBeVisible();
});

test("confirm and manage pages explain a bad link", async ({ page }) => {
  await page.goto("/guest/confirm?token=bogus");
  await expect(page.getByRole("heading", { name: "Confirm your registration" })).toBeVisible();
  await page.getByRole("button", { name: "Confirm my registration" }).click();
  await expect(page.getByText(/expired or was already used/i)).toBeVisible();

  await page.goto("/guest/confirm");
  await expect(page.getByText("This page needs the link from your email.")).toBeVisible();

  await page.goto("/guest/manage/bogus");
  await expect(page.getByRole("heading", { name: "Your guest registration" })).toBeVisible();
  await expect(page.getByText(/expired or was already used/i)).toBeVisible();
});
