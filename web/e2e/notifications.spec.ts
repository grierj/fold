import { expect, test } from "@playwright/test";
import { apiAs, email, signIn } from "./helpers";

// M1.1c: stored notifications. The badge counts waiting items, inline
// Approve resolves the task, and archive (with Undo) is stored on the
// server, so it survives signing out.

test("a waiting group approval is counted and approved from the bell", async ({ page, request }) => {
  type Badge = { unread: number; waiting: number };
  const badgeAccessibleName = (badge: Badge) => {
    const parts = [];
    if (badge.unread > 0) parts.push(`${badge.unread} unread`);
    if (badge.waiting > 0) parts.push(`${badge.waiting} waiting on you`);
    const label = parts.length > 0 ? `Alerts, ${parts.join(", ")}` : "Alerts";
    const count = badge.unread + badge.waiting;
    return count > 0 ? `${count > 9 ? "9+" : count} ${label}` : label;
  };
  const admin = await apiAs(request, "grier");
  const baseline = await admin("GET", "/notifications/badge") as Badge;
  const sam = await apiAs(request, "sam");
  const { group } = await sam("POST", "/groups", {
    name: "Bell study", description: "", tags: [], imageUrl: "", location: "", visibility: "private", memberLimit: null,
  }) as { group: { id: number } };

  await signIn(page, "grier");
  const readBadge = async (): Promise<Badge> => {
    const response = await page.request.get("/api/v1/notifications/badge");
    expect(response.ok()).toBe(true);
    return (await response.json()) as Badge;
  };
  await expect.poll(async () => (await readBadge()).waiting).toBe(baseline.waiting + 1);
  const createdBadge = await readBadge();
  expect(createdBadge).toEqual({ unread: baseline.unread, waiting: baseline.waiting + 1 });

  const waitingResponse = await page.request.get("/api/v1/notifications?filter=waiting");
  expect(waitingResponse.ok()).toBe(true);
  const waitingFeed = await waitingResponse.json() as {
    notifications: { id: number; subjectId?: number; title: string; waiting: boolean }[];
  };
  const createdTask = waitingFeed.notifications.find((notification) => notification.subjectId === group.id);
  expect(createdTask).toMatchObject({ title: "Sam Member started a group: Bell study", waiting: true });

  const bell = page.getByRole("button", { name: /Alerts/ });
  await expect(bell).toHaveAccessibleName(badgeAccessibleName(createdBadge));
  await bell.click();
  const panel = page.locator("#notif-panel");
  await expect(panel.getByRole("heading", { name: "Waiting on you" })).toBeVisible();
  await expect(panel.getByRole("button", { name: "Mark all as read" })).toHaveAttribute(
    "aria-disabled", String(createdBadge.unread === 0)
  );
  const row = panel.getByRole("listitem").filter({ hasText: "Sam Member started a group: Bell study" });
  await expect(row).toHaveCount(1);
  await expect(row.getByText("Sam Member started a group: Bell study", { exact: true })).toBeVisible();
  const approved = page.waitForResponse((response) =>
    new URL(response.url()).pathname === `/api/v1/admin/groups/${group.id}/approve` && response.request().method() === "POST"
  );
  await row.getByRole("button", { name: "Approve" }).click();
  expect((await approved).ok()).toBe(true);
  await expect(row).toContainText("You approved this");
  await expect.poll(async () => (await readBadge()).waiting).toBe(baseline.waiting);
  const resolvedBadge = await readBadge();
  expect(resolvedBadge).toEqual({ unread: baseline.unread + 1, waiting: baseline.waiting });
  await expect(bell).toHaveAccessibleName(badgeAccessibleName(resolvedBadge));

  const resolvedResponse = await page.request.get("/api/v1/notifications?filter=all");
  expect(resolvedResponse.ok()).toBe(true);
  const resolvedFeed = await resolvedResponse.json() as {
    notifications: { id: number; subjectId?: number; waiting: boolean; resolution?: { outcome: string; byMe: boolean } }[];
  };
  const resolvedTask = resolvedFeed.notifications.find((notification) => notification.id === createdTask!.id);
  expect(resolvedTask).toMatchObject({
    subjectId: group.id,
    waiting: false,
    resolution: { outcome: "approved", byMe: true },
  });
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
  const bell = page.getByRole("button", { name: /Alerts/ });
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

  await signIn(page, "grier");
  await page.goto("/notifications");
  await expect(page.getByRole("heading", { name: "Alerts", level: 1 })).toBeVisible();
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
  test("the Alerts tab opens the notifications page", async ({ page }) => {
    await signIn(page, "grier");
    await page.getByRole("navigation", { name: "Tabs" }).getByRole("link", { name: /Alerts/ }).click();
    await expect(page).toHaveURL(/\/notifications$/);
    await expect(page.getByRole("button", { name: "All", pressed: true })).toBeVisible();
  });
});
