import { expect, test } from "@playwright/test";
import { apiAs } from "./helpers";

// Tag filters: any selected tag matches (OR), Clear resets, and a link that
// opens with tags selected still offers every tag.
test("tag filters OR together, clear, and deep-link with all chips", async ({ page, request }) => {
  const admin = await apiAs(request, "grier");
  for (const [name, tags] of [["Choir practice", ["choir"]], ["Band night", ["Band"]], ["Women's study", ["women"]]] as const) {
    await admin("POST", "/groups", {
      name, description: "", tags, imageUrl: "", location: "", visibility: "public", memberLimit: null,
    });
  }
  const directory = page.locator("#groups");
  const chips = page.getByRole("group", { name: "Group tags", exact: true });
  const chip = (name: string) => chips.getByRole("button", { name, exact: true });
  const group = (name: string) => page.getByRole("heading", { name });

  await page.goto("/");
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await chip("choir").click();
  await chip("Band").click();
  await expect(page).toHaveURL(/tag=choir&tag=Band/);
  await expect(group("Choir practice")).toBeVisible();
  await expect(group("Band night")).toBeVisible();
  await expect(group("Women's study")).toHaveCount(0);
  await expect(group("Room 201 class")).toHaveCount(0);
  await expect(directory.getByRole("button", { name: "Remove choir filter" })).toBeVisible();
  await expect(directory.getByRole("button", { name: "Remove Band filter" })).toBeVisible();

  const cleared = page.waitForResponse((r) => r.request().method() === "GET" && new URL(r.url()).pathname === "/api/v1/groups" && !new URL(r.url()).search);
  await directory.getByRole("button", { name: "Clear", exact: true }).click();
  await expect(page).toHaveURL("/");
  const total = ((await (await cleared).json()) as { groups: unknown[] }).groups.length;
  // Wait for the unfiltered result, not the previous two-group count that
  // can survive briefly while the URL change starts its request.
  await expect(directory.getByRole("status")).toHaveText(total > 12 ? `Showing 12 of ${total} groups` : `${total} groups`);
  // Clear restores every group, not necessarily all on the first 12-row batch.
  const more = directory.getByRole("button", { name: "Show more groups", exact: true });
  while (await more.count()) {
    const before = await directory.getByRole("status").innerText();
    await more.click();
    await expect(directory.getByRole("status")).not.toHaveText(before);
  }
  await expect(group("Women's study")).toBeVisible();
  await expect(group("Room 201 class")).toBeVisible();
  await expect(page.getByRole("group", { name: "Selected group filters" })).toHaveCount(0);

  // A shared link with one tag selected still shows every chip. The URL's
  // case doesn't matter.
  await page.goto("/?tag=CHOIR");
  await page.getByRole("button", { name: "Filters (1)", exact: true }).click();
  await expect(group("Choir practice")).toBeVisible();
  await expect(group("Band night")).toHaveCount(0);
  for (const name of ["Band", "class", "women"]) {
    await expect(chip(name)).toHaveAttribute("aria-pressed", "false");
  }
  await expect(chips.getByRole("button", { pressed: true })).toHaveCount(1);
});
