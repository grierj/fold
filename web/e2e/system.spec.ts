import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

// M3.1b: Admin → System lists background tasks that kept failing. The tab says
// "Nothing needs attention." only when no task is dead and no email needs a
// word (S3-e). This server has no mail provider, so any notice another spec
// in the shard caused is stored as failed ("email isn't set up"), and a notice
// still waiting is queued until the notify.deliver job runs. The shard's
// history decides which of the two a fresh run sees, so the spec reads the
// server's own counts, waits for deliveries to settle, and asserts the page
// says what those counts say. Retry and Dismiss are covered by Vitest
// (SystemTab.test.tsx) and the Go handler tests: e2e can't seed a dead job
// through the API.
type Summary = { jobs: unknown[]; more: boolean; deliveries: { queued: number; deferred: number; failed: number } };

test("the System tab reports background tasks and email truthfully", async ({ page }) => {
  await signIn(page, "grier");
  const load = async () => (await (await page.request.get("/api/v1/admin/jobs")).json()) as Summary;

  // Queued emails drain on their own within a few polls of the job worker.
  await expect.poll(async () => (await load()).deliveries.queued, { timeout: 15_000 }).toBe(0);
  const before = await load();

  await page.goto("/admin");
  await page.getByRole("tab", { name: /^System\b/ }).click();
  await expect(page).toHaveURL(/tab=system/);
  await expect(page.getByRole("heading", { name: "Background tasks" })).toBeVisible();

  const quiet = before.jobs.length === 0 && before.deliveries.deferred === 0 && before.deliveries.failed === 0;
  if (quiet) {
    await expect(page.getByText("Nothing needs attention.")).toBeVisible();
    await expect(page.getByText("Background tasks are running normally.")).toBeVisible();
    return;
  }
  // Something is worth saying, so the empty state must not claim otherwise.
  await expect(page.getByText("Nothing needs attention.")).toHaveCount(0);
  if (before.deliveries.failed > 0) {
    await expect(page.getByText(/could not be sent in the last 7 days/)).toBeVisible();
    await expect(page.getByText(/email isn't set up/)).toBeVisible();
  }
});
