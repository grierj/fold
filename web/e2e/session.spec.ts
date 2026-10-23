import { expect, test } from "@playwright/test";
import { apiAs, signIn, signOut } from "./helpers";

// Grier's Oct 10 reports: logout left the previous session on screen, and
// an admin signing in after a member created a group saw no notice.

test("logout clears the session's data, including through back", async ({ page }) => {
  await signIn(page, "sam");
  await expect(page.getByRole("heading", { name: "Members-only circle" })).toBeVisible();
  await page.getByRole("heading", { name: "Invite-only circle" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Invite-only circle" })).toBeVisible();
  // Back to the listing, where Grier was when the stale screen stayed up.
  await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Home" }).click();
  await expect(page.getByRole("heading", { name: "Members-only circle" })).toBeVisible();
  await page.evaluate(() => sessionStorage.setItem("probe", "x"));
  const session = async () => (await page.context().cookies()).find((c) => c.name === "fold-session")?.value;
  const before = await session();

  await signOut(page);
  await expect(page.getByRole("heading", { name: "Room 201 class" })).toBeVisible();
  for (const text of ["Members-only circle", "Invite-only circle", "Sam Member", "Log out"]) {
    await expect(page.getByText(text)).toHaveCount(0);
  }
  expect(await page.evaluate(() => localStorage.length + sessionStorage.length)).toBe(0);
  // The server session is over: the old cookie is gone (the fresh page got
  // a new anonymous one for its CSRF token) and the API sees no one.
  expect(await session()).not.toBe(before);
  expect((await page.request.get("/api/v1/auth/me")).status()).toBe(401);

  // Back to the invite-only group's URL, then the listing before it: each
  // loads fresh and signed out.
  for (const url of [/\/groups\/\d+$/, /\/$/]) {
    await page.goBack();
    await expect(page).toHaveURL(url);
    await expect(page.getByRole("link", { name: "Log in" })).toBeVisible();
    for (const text of ["Members-only circle", "Invite-only circle", "Sam Member"]) {
      await expect(page.getByText(text)).toHaveCount(0);
    }
  }
});

test("the shell and API responses are never stored", async ({ page, request }) => {
  const shell = await page.goto("/groups/1");
  expect(shell?.headers()["cache-control"]).toBe("no-store");
  expect((await request.get("/api/v1/groups")).headers()["cache-control"]).toBe("no-store");
});

test("an admin signing in after a member's new group sees the notice", async ({ page, request }) => {
  await signIn(page, "sam");
  await page.goto("/groups/new");
  await page.getByLabel("Group name").fill("Sam's new study");
  await page.getByRole("button", { name: "Create group" }).click();
  await expect(page.getByRole("heading", { name: "Pending approval" })).toBeVisible();
  await signOut(page);

  await signIn(page, "grier");
  const bell = page.getByRole("button", { name: /Notifications/ });
  await expect(bell).toHaveAccessibleName(/waiting on you/);
  await bell.click();
  const notice = page.getByRole("link", { name: /Sam Member started a group: Sam's new study/ });
  await expect(notice).toBeVisible();

  // Opening it doesn't clear a task. Another member's group arriving while
  // signed in is its own row on the next navigation.
  await notice.click();
  await expect(page).toHaveURL(/tab=approvals/);
  const sam = await apiAs(request, "sam");
  await sam("POST", "/groups", { name: "Another study", description: "", tags: [], imageUrl: "", location: "", visibility: "public", memberLimit: null });
  await page.getByRole("link", { name: "My groups" }).click();
  await bell.click();
  await expect(page.getByRole("link", { name: /started a group: Another study/ })).toBeVisible();
  await expect(page.getByRole("link", { name: /started a group: Sam's new study/ })).toBeVisible();

  // Leave nothing pending for other specs.
  const again = await apiAs(request, "grier"); // the request context is Sam's now
  const left = await again("GET", "/admin/pending");
  for (const g of left.groups) await again("POST", `/admin/groups/${g.id}/approve`, {});
});

test("an admin creating a group sees Group created", async ({ page }) => {
  await signIn(page, "grier");
  await page.goto("/groups/new");
  await page.getByLabel("Group name").fill("Admin's study");
  await page.getByRole("button", { name: "Create group" }).click();
  await expect(page.getByRole("heading", { name: "Group created" })).toBeVisible();
  await expect(page.getByText("Pending approval")).toHaveCount(0);
  await page.getByRole("link", { name: "Go to the group" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Admin's study" })).toBeVisible();
});
