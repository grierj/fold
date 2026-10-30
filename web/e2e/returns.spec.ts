import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

// Re-enabled into a full group (DEC-47): Pat and Quinn were left out and
// told to ask a leader. There's no waiting list; a leader adds by email
// as an explicit over-limit choice.
test("left-out account is told to ask a leader, who adds them over the limit", async ({ page }) => {
  page.on("dialog", (d) => d.accept());

  await signIn(page, "pat");
  await page.getByRole("button", { name: "Alerts" }).click();
  await expect(page.getByText("You're not back in Room 201 class yet")).toBeVisible();
  await expect(page.getByText("Ask a leader to add you.")).toBeVisible();

  await page.context().clearCookies();
  await signIn(page, "lee");
  await page.goto("/groups/1");
  await expect(page.getByText("3 of 3 members")).toBeVisible();
  const membersTab = page.getByRole("tablist", { name: "Group sections", exact: true })
    .getByRole("tab", { name: "Members", exact: true });
  await membersTab.click();
  await expect(membersTab).toHaveAttribute("aria-selected", "true");
  const membersPanel = page.getByRole("tabpanel", { name: "Members", exact: true });
  await expect(membersPanel).toBeVisible();
  await expect(page.locator("#waiting")).toHaveCount(0);
  await expect(page.getByText("Pat Returner")).toHaveCount(0);
  // One Add people panel (DEC-99): the email path is one click from the name search.
  await page.getByRole("button", { name: "Add people" }).click();
  await page.getByRole("button", { name: "Use an email instead" }).click();
  await page.getByLabel("Email address").fill("pat@example.com");
  await page.getByRole("button", { name: "Add member", exact: true }).click();
  await expect(page.getByRole("listitem").filter({ hasText: "Pat Returner" })).toBeVisible();
  await expect(page.getByText("4 of 3 members")).toBeVisible();
});
