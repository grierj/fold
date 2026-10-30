import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

// A leader sees admins labeled and gets no Remove for them (DEC-38).
test("leader roster labels the admin and offers no Remove for them", async ({ page }) => {
  await signIn(page, "lee");
  await page.goto("/groups/1");
  const sections = page.getByRole("tablist", { name: "Group sections", exact: true });
  const eventsTab = sections.getByRole("tab", { name: "Upcoming events", exact: true });
  const membersTab = sections.getByRole("tab", { name: "Members", exact: true });
  await expect(eventsTab).toHaveAttribute("aria-selected", "true");
  await eventsTab.focus();
  await eventsTab.press("End");
  await expect(membersTab).toBeFocused();
  await expect(eventsTab).toHaveAttribute("aria-selected", "false");
  await expect(membersTab).toHaveAttribute("aria-selected", "true");
  const membersPanel = page.getByRole("tabpanel", { name: "Members", exact: true });
  await expect(membersPanel).toBeVisible();
  await expect(membersTab).toHaveAttribute("aria-controls", await membersPanel.getAttribute("id") as string);

  const grier = page.getByRole("listitem").filter({ hasText: "Grier" });
  await expect(grier.getByText("Admin", { exact: true })).toBeVisible();
  const grierMore = grier.getByRole("button", { name: /^More for/ });
  if (await grierMore.count()) await grierMore.click();
  await expect(grier.getByRole("menuitem", { name: "Remove from group" })).toHaveCount(0);
  await expect(grier.getByRole("menuitem", { name: "Remove as leader" })).toHaveCount(0);
  await page.keyboard.press("Escape");

  const sam = page.getByRole("listitem").filter({ hasText: "Sam Member" });
  await expect(sam.getByText("Admin", { exact: true })).toHaveCount(0);
  await sam.getByRole("button", { name: /^More for/ }).click();
  await expect(sam.getByRole("menuitem", { name: "Remove from group" })).toBeVisible();
});
