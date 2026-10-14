import { expect, test } from "@playwright/test";
import { apiAs, email, signIn, signOut } from "./helpers";

// M1.1c: stored notifications. The badge counts waiting items, inline
// Approve resolves the task, and archive (with Undo) is stored on the
// server, so it survives signing out.

test("a waiting group approval is counted and approved from the bell", async ({ page, request }) => {
  const sam = await apiAs(request, "sam");
  await sam("POST", "/groups", { name: "Bell study", description: "", tags: [], imageUrl: "", location: "", visibility: "private", memberLimit: null });

  await signIn(page, "grier");
  const bell = page.getByRole("button", { name: /^Notifications/ });
  await expect(bell).toHaveAccessibleName(/1 waiting on you/);
  await bell.click();
  const panel = page.locator("#notif-panel");
  await expect(panel.getByRole("heading", { name: "Waiting on you" })).toBeVisible();
  // Nothing ordinary is unread, so Mark all as read is off; the task stays.
  await expect(panel.getByRole("button", { name: "Mark all as read" })).toHaveAttribute("aria-disabled", "true");

  const row = panel.getByRole("listitem").filter({ hasText: "started a group: Bell study" });
  const approved = page.waitForResponse((r) => r.url().includes("/approve") && r.request().method() === "POST");
  await row.getByRole("button", { name: "Approve" }).click();
  expect((await approved).ok()).toBe(true);
  await expect(row).toContainText("You approved this");
  await expect(bell).not.toHaveAccessibleName(/waiting on you/);
});

test("archive survives signing out, and Undo or the Archived filter restores it", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  const { users } = (await admin("GET", "/admin/users")) as { users: { id: number; email: string }[] };
  const id = (name: string) => users.find((u) => u.email === email(name))!.id;
  const { group } = (await admin("POST", "/groups", {
    name: "Notice club", description: "", tags: [], imageUrl: "", location: "", visibility: "private", memberLimit: null,
  })) as { group: { id: number } };
  await admin("POST", `/admin/groups/${group.id}/members`, { userId: id("lee"), role: "leader" });
  await admin("POST", `/admin/groups/${group.id}/members`, { userId: id("sam"), role: "member" });
  // Lee (not an admin) makes Sam a leader: the admins hear about it.
  await (await apiAs(request, "lee"))("PUT", `/groups/${group.id}/members/${id("sam")}/role`, { role: "leader" });

  await signIn(page, "grier");
  const bell = page.getByRole("button", { name: /^Notifications/ });
  await expect(bell).toHaveAccessibleName(/unread/);
  await bell.click();
  const panel = page.locator("#notif-panel");
  const title = "Lee Leader made Sam Member a leader";
  await expect(panel.getByRole("link", { name: new RegExp(title) })).toBeVisible();

  await panel.getByRole("button", { name: "Mark all as read" }).click();
  await expect(bell).not.toHaveAccessibleName(/unread/);

  await panel.getByRole("button", { name: `Archive: ${title}` }).click();
  await expect(panel.getByRole("link", { name: new RegExp(title) })).toHaveCount(0);
  await page.getByRole("button", { name: "Undo" }).click();
  await expect(panel.getByRole("link", { name: new RegExp(title) })).toBeVisible();

  await panel.getByRole("button", { name: `Archive: ${title}` }).click();
  await expect(panel.getByRole("link", { name: new RegExp(title) })).toHaveCount(0);
  expect(await page.evaluate(() => localStorage.length + sessionStorage.length)).toBe(0);

  await signOut(page);
  await signIn(page, "grier");
  await page.goto("/notifications");
  await expect(page.getByRole("heading", { name: "Notifications", level: 1 })).toBeVisible();
  await expect(page.getByRole("link", { name: new RegExp(title) })).toHaveCount(0);
  await page.getByRole("button", { name: "Archived" }).click();
  await expect(page).toHaveURL(/filter=archived/);
  await expect(page.getByRole("link", { name: new RegExp(title) })).toBeVisible();
  await page.getByRole("button", { name: `Restore: ${title}` }).click();
  await expect(page.getByRole("link", { name: new RegExp(title) })).toHaveCount(0);
  await page.getByRole("button", { name: "All" }).click();
  await expect(page.getByRole("link", { name: new RegExp(title) })).toBeVisible();
});

test.describe("on a phone", () => {
  test.use({ viewport: { width: 390, height: 800 } });
  test("the bell opens the notifications page", async ({ page }) => {
    await signIn(page, "grier");
    await page.getByRole("button", { name: /^Notifications/ }).click();
    await expect(page).toHaveURL(/\/notifications$/);
    await expect(page.getByRole("button", { name: "All", pressed: true })).toBeVisible();
  });
});
