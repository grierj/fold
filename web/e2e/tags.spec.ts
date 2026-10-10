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
  const chips = page.locator("section").first();
  const chip = (name: string) => chips.getByRole("button", { name, exact: true });
  const group = (name: string) => page.getByRole("heading", { name });

  await page.goto("/");
  await chip("choir").click();
  await chip("Band").click();
  await expect(page).toHaveURL(/tag=choir&tag=Band/);
  await expect(group("Choir practice")).toBeVisible();
  await expect(group("Band night")).toBeVisible();
  await expect(group("Women's study")).toHaveCount(0);
  await expect(group("Room 201 class")).toHaveCount(0);
  await expect(page.getByText("Showing groups tagged choir or Band.")).toBeVisible();

  await chips.getByRole("button", { name: "Clear" }).click();
  await expect(page).toHaveURL("/");
  await expect(group("Women's study")).toBeVisible();
  await expect(group("Room 201 class")).toBeVisible();
  await expect(page.getByText("Showing groups tagged")).toHaveCount(0);

  // A shared link with one tag selected still shows every chip. The URL's
  // case doesn't matter.
  await page.goto("/?tag=CHOIR");
  await expect(group("Choir practice")).toBeVisible();
  await expect(group("Band night")).toHaveCount(0);
  for (const name of ["Band", "class", "women"]) {
    await expect(chip(name)).toHaveAttribute("aria-pressed", "false");
  }
  await expect(chips.getByRole("button", { pressed: true })).toHaveCount(1);
});
