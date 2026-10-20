import { test } from "@playwright/test";
import { apiAs, signIn } from "./helpers";

const OUT = "/workspace/fold-ui-audit/m17-screens/wright";
test("dec-68 calendar shots", async ({ page, request }) => {
  test.setTimeout(120000);
  const admin = await apiAs(request, "grier");
  const soon = new Date(Date.now() + 2 * 24 * 60 * 60 * 1000);
  const pad = (n: number) => String(n).padStart(2, "0");
  const startsLocal = `${soon.getFullYear()}-${pad(soon.getMonth() + 1)}-${pad(soon.getDate())}T18:00`;
  const { group } = await admin("POST", "/groups", { name: "Closed Choir", description: "", tags: [], imageUrl: "", location: "", visibility: "public", memberLimit: null });
  await admin("POST", `/groups/${group.id}/events`, { title: "Old Choir Night", startsLocal, durationMinutes: 60, tzid: "America/Boise" });
  await admin("DELETE", `/admin/groups/${group.id}`);
  await signIn(page, "grier");
  for (const [w, h] of [[1280, 800], [390, 844]]) {
    await page.setViewportSize({ width: w, height: h });
    await page.goto("/calendar");
    await page.getByRole("button", { name: "Show deleted events" }).waitFor();
    await page.screenshot({ path: `${OUT}/calendar-deleted-off-${w}.png` });
    await page.getByRole("button", { name: "Show deleted events" }).click();
    await page.getByText(/\(deleted group\)/).first().waitFor();
    await page.getByText(/\(deleted group\)/).first().scrollIntoViewIfNeeded();
    await page.screenshot({ path: `${OUT}/calendar-deleted-on-${w}.png` });
  }
});
