import AxeBuilder from "@axe-core/playwright";
import { mkdirSync } from "node:fs";
import { expect, test, type Locator, type Page, type Request, type Route } from "@playwright/test";
import { apiAs, email, fromMore, nextFrames, PASSWORD, signInTo } from "./helpers";
// @ts-expect-error plain JS module shared with verify-ui
import { measure } from "./ui-measure.mjs";

// The delete panel's outcomes (M7.4 T07B-1c). Each press of Delete mints an
// attempt id and sends it with the DELETE. These cases make the answer to that
// DELETE uncertain, expired or busy, against the real server, and check what
// the panel says and which id it sends next. Every scenario makes its own
// person, so this spec depends on no other spec and no seed user besides the
// admin.
//
// The server decides an id's age on its own monotonic clock, which a browser
// cannot move. The test binary (built with -tags e2eclock, see
// attempt_clock_e2e.go) adds an offset to that clock through an admin-only route
// that exists in no other build and answers only when server.sh sets
// FOLD_E2E_CLOCK=1. The age cases mint a valid id, move the clock, and let the
// page submit and settle that same unchanged id. When the binary has no such
// route those cases fail (E2E_NO_CLOCK=1 skips them on purpose).

const adminName = "grier";
const proof = process.env.SCREENS_DIR;
const WIDTHS = [390, 1280] as const;

const MINT = "/api/v1/admin/operation-attempts";
const SETTLE = "/api/v1/admin/operation-attempts/settle";

const NOTHING = "Nothing was deleted by this attempt.";
const DELETED = "The account was deleted.";
const HISTORY = "Fold can't tell whether an earlier delete finished. Reload this person's page to check.";
const TOO_OLD = "This delete request was too old to run, so Fold didn't run it.";
const CHECKING = "Checking what happened to this delete request…";
const RESTORE = "Fold noticed its database was restored while it was running, so nothing was changed. It is restarting; try again in a minute.";
const BUSY =
  "Someone was changing this person's household at the same moment, so nothing was deleted. Try again in a moment.";
const NOT_DELETED = /nothing was deleted|not deleted|wasn't deleted|was not deleted/i;
// R4-6c: what a delete does not reach, and what the two counts mean. Landed code
// still retains network addresses in the activity log (inventory IR-1 to IR-4).
const BACKUPS_NOTE = /Deleting reaches Fold's backups only as they expire/;
const OUTSIDE_NOTE = /Copies already sent to mailboxes, mail providers and push services are outside Fold/;
const IP_NOTE = /Some network \(IP\) addresses can remain in the activity log/;
const KEPT_HOUSEHOLDS_NOTE = /Fold left these households as they were/;
const KEPT_ACTIVITY_NOTE = /Fold counts entries, not addresses/;

type Admin = Awaited<ReturnType<typeof apiAs>>;
type Person = { id: number; email: string; name: string };

function recordOf(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw new Error("expected a JSON object");
  return Object.fromEntries(Object.entries(value));
}

function numberAt(value: unknown, ...path: string[]): number {
  let at: unknown = value;
  for (const key of path) at = recordOf(at)[key];
  if (typeof at !== "number") throw new Error(`expected a number at ${path.join(".")}`);
  return at;
}

async function makePerson(admin: Admin, login: string, name: string): Promise<Person> {
  const made = await admin("POST", "/admin/users", { ageBand: "adult", name, email: email(login), password: PASSWORD, role: "member" });
  return { id: numberAt(made, "user", "id"), email: email(login), name };
}

// Opens Delete for `target`. With `expectReady` false it does not wait for the
// button to turn on (a person whose delete is held off never does).
async function openDelete(page: Page, target: Person, expectReady = true): Promise<Locator> {
  await page.getByRole("searchbox", { name: "Search by name or email" }).fill(target.email);
  const row = page.getByRole("row").filter({ hasText: target.email });
  await fromMore(row, "Delete");
  if (expectReady) await expect(row.getByRole("button", { name: "Delete account" })).toBeEnabled();
  return row;
}

// The attempt ids of every mint, DELETE and settle the page sends, in order.
// An id is a capability, so it is compared here and never printed.
function watchIds(page: Page, targetId: number) {
  const ids = { minted: 0, deleted: [] as string[], settled: [] as string[] };
  const onRequest = (r: Request) => {
    const path = new URL(r.url()).pathname;
    if (r.method() === "POST" && path === MINT) ids.minted++;
    const body = r.postData() ? recordOf(JSON.parse(r.postData() ?? "{}")) : {};
    if (r.method() === "DELETE" && path === `/api/v1/admin/users/${targetId}` && typeof body.attemptId === "string") ids.deleted.push(body.attemptId);
    if (r.method() === "POST" && path === SETTLE && typeof body.attemptId === "string") ids.settled.push(body.attemptId);
  };
  page.on("request", onRequest);
  return { ids, stop: () => page.off("request", onRequest) };
}

const deletePath = (target: Person) => `**/api/v1/admin/users/${target.id}`;

// The DELETE answers with `answer` instead of its real response; with `run`,
// the real DELETE is sent first (so the account really is deleted) and its
// response is dropped, which is what a lost connection looks like.
async function answerFirstDelete(page: Page, target: Person, answer: { status: number; code: string }, run: boolean) {
  let first = true;
  await page.route(deletePath(target), async (route: Route) => {
    if (route.request().method() !== "DELETE" || !first) return route.fallback();
    first = false;
    if (run) await route.fetch();
    await route.fulfill({ status: answer.status, json: { error: "server text the page must not show", code: answer.code } });
  });
}

// Holds the settle call until `release`, so the checking state can be read.
async function holdSettle(page: Page) {
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  await page.route(`**${SETTLE}`, async (route) => {
    await gate;
    await route.fallback();
  });
  return release;
}

// The attempt clock of the test binary (attempt_clock_e2e.go): how many seconds
// ahead of real time the server counts an id's age.
async function setClock(admin: Admin, offsetSeconds: number) {
  await admin("POST", "/e2e/attempt-clock", { offsetSeconds });
}

// The age cases are required acceptance in the standard harness (run.sh and
// server.sh build the clock in and turn it on), so a clock that cannot be set
// fails the test with the reason. Only an explicit E2E_NO_CLOCK=1, for running
// against some other binary, skips them.
async function requireClock(admin: Admin) {
  test.skip(process.env.E2E_NO_CLOCK === "1", "E2E_NO_CLOCK=1: this binary has no attempt clock");
  await setClock(admin, 0);
}

// Moves the clock the moment the page's first DELETE leaves for the server, so
// the id minted a moment earlier is that many seconds old when it arrives.
async function ageFirstDelete(page: Page, target: Person, admin: Admin, seconds: number) {
  let first = true;
  await page.route(deletePath(target), async (route) => {
    if (route.request().method() !== "DELETE" || !first) return route.fallback();
    first = false;
    await setClock(admin, seconds);
    await route.fallback();
  });
}

// One state, at 390 and 1280: axe and layout checks first, then the screenshot
// when SCREENS_DIR asks for one. Call it only after the state's own assertions
// have passed.
async function evidence(page: Page, state: string) {
  for (const width of WIDTHS) {
    await page.setViewportSize({ width, height: 900 });
    await page.evaluate(() => window.scrollTo(0, 0));
    await nextFrames(page);
    const wcag = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
    const headings = await new AxeBuilder({ page }).withRules(["heading-order"]).analyze();
    const bad = [...wcag.violations, ...headings.violations].filter((v) => v.impact === "moderate" || v.impact === "serious" || v.impact === "critical");
    expect(bad.map((v) => `${v.impact} ${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`), `${state} at ${width}px`).toEqual([]);
    const layout = await page.evaluate(measure);
    expect(layout.overflow, `${state} at ${width}px: sideways overflow`).toBeLessThanOrEqual(0);
    expect(layout.clipped, `${state} at ${width}px: clipped select`).toEqual([]);
    expect(layout.under24, `${state} at ${width}px: targets under 24px`).toEqual([]);
    if (proof) {
      mkdirSync(proof, { recursive: true });
      await page.screenshot({ path: `${proof}/admin-delete-user-${state}-${width}.png`, fullPage: true, animations: "disabled" });
    }
  }
  await page.setViewportSize({ width: 1280, height: 900 });
}

test.describe("what the delete panel says when an answer is lost, too old or busy", () => {
  test.use({ locale: "en-US", timezoneId: "America/Boise" });
  test.setTimeout(120_000);

  test("a lost answer to a delete that ran settles to deleted, and Done closes the row", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const admin = await apiAs(request, adminName);
    const target = await makePerson(admin, `unknown-ran-${suffix}`, `Una Known ${suffix}`);
    await signInTo(page, adminName, "/admin?tab=users");
    const watch = watchIds(page, target.id);
    const row = await openDelete(page, target);
    await answerFirstDelete(page, target, { status: 500, code: "delete_outcome_unknown" }, true);
    const release = await holdSettle(page);

    await row.getByRole("button", { name: "Delete account" }).click();
    const status = row.getByRole("status");
    await expect(status).toHaveText(CHECKING);
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    await expect(row.getByText(NOT_DELETED)).toHaveCount(0);
    await expect(row.getByText("server text the page must not show")).toHaveCount(0);
    await evidence(page, "checking");

    release();
    await expect(row.getByText(DELETED)).toBeVisible();
    await expect(row.getByRole("button", { name: "Delete account" })).toHaveCount(0);
    await expect(status).toBeFocused();
    await evidence(page, "settled-deleted");
    await row.getByRole("button", { name: "Done" }).click();
    await expect(page.getByRole("row").filter({ hasText: target.email })).toHaveCount(0);

    watch.stop();
    expect(watch.ids.minted).toBe(1);
    expect(watch.ids.deleted).toHaveLength(1);
    expect(watch.ids.settled).toEqual(watch.ids.deleted);
  });

  test("a lost answer to a delete that never ran says nothing was deleted by this attempt, then a fresh press deletes", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const admin = await apiAs(request, adminName);
    const target = await makePerson(admin, `unknown-didnt-${suffix}`, `Nora Ran ${suffix}`);
    await signInTo(page, adminName, "/admin?tab=users");
    const watch = watchIds(page, target.id);
    const row = await openDelete(page, target);
    await answerFirstDelete(page, target, { status: 500, code: "delete_outcome_unknown" }, false);

    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByText(NOTHING)).toBeVisible();
    await expect(row.getByRole("button", { name: "Delete account" })).toBeEnabled();
    await expect(page.getByRole("row").filter({ hasText: target.email })).toHaveCount(1);
    await evidence(page, "not-applied");

    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByText(DELETED)).toBeVisible();
    await expect(row.getByText("Households kept", { exact: true })).toBeVisible();
    await expect(row.getByText("Activity entries kept", { exact: true })).toBeVisible();
    await expect(row.getByText(KEPT_HOUSEHOLDS_NOTE)).toBeVisible();
    await expect(row.getByText(KEPT_ACTIVITY_NOTE)).toBeVisible();
    await expect(row.getByText(IP_NOTE)).toBeVisible();
    await evidence(page, "done-with-counts");
    await row.getByRole("button", { name: "Done" }).click();
    await expect(page.getByRole("row").filter({ hasText: target.email })).toHaveCount(0);

    watch.stop();
    expect(watch.ids.minted).toBe(2);
    expect(watch.ids.deleted).toHaveLength(2);
    expect(watch.ids.deleted[0]).not.toBe(watch.ids.deleted[1]);
    expect(watch.ids.settled).toEqual([watch.ids.deleted[0]]);
  });

  test("before the press the panel says what a delete does not reach, and a plain delete keeps saying it", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const admin = await apiAs(request, adminName);
    const target = await makePerson(admin, `reach-${suffix}`, `Rae Reach ${suffix}`);
    await signInTo(page, adminName, "/admin?tab=users");
    const row = await openDelete(page, target);

    await expect(row.getByText(BACKUPS_NOTE)).toBeVisible();
    await expect(row.getByText(OUTSIDE_NOTE)).toBeVisible();
    await expect(row.getByText(IP_NOTE)).toBeVisible();
    await expect(row.getByRole("button", { name: "Delete account" })).toBeEnabled();
    await expect(row.getByText("Households kept", { exact: true })).toHaveCount(0);
    await evidence(page, "reach-before-delete");

    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByText(DELETED)).toBeVisible();
    await expect(row.getByText(KEPT_HOUSEHOLDS_NOTE)).toBeVisible();
    await expect(row.getByText(KEPT_ACTIVITY_NOTE)).toBeVisible();
    await expect(row.getByText(BACKUPS_NOTE)).toBeVisible();
    await expect(row.getByText(IP_NOTE)).toBeVisible();
    await expect(row.getByText(NOT_DELETED)).toHaveCount(0);
    await evidence(page, "reach-after-delete");
    await row.getByRole("button", { name: "Done" }).click();
    await expect(page.getByRole("row").filter({ hasText: target.email })).toHaveCount(0);
  });

  test("a valid id older than ten minutes says Fold did not run it, settles that same id to nothing deleted, and a fresh press deletes", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const admin = await apiAs(request, adminName);
    await requireClock(admin);
    const target = await makePerson(admin, `expired-${suffix}`, `Ed Expired ${suffix}`);
    try {
      await signInTo(page, adminName, "/admin?tab=users");
      const watch = watchIds(page, target.id);
      const row = await openDelete(page, target);
      await ageFirstDelete(page, target, admin, 11 * 60);
      const release = await holdSettle(page);

      await row.getByRole("button", { name: "Delete account" }).click();
      await expect(row.getByRole("status")).toContainText(TOO_OLD);
      await expect(row.getByText(NOT_DELETED)).toHaveCount(0);
      await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
      await evidence(page, "expired");

      release();
      await expect(row.getByText(NOTHING)).toBeVisible();
      await expect(row.getByRole("button", { name: "Delete account" })).toBeEnabled();
      await row.getByRole("button", { name: "Delete account" }).click();
      await expect(row.getByText(DELETED)).toBeVisible();
      await row.getByRole("button", { name: "Done" }).click();
      await expect(page.getByRole("row").filter({ hasText: target.email })).toHaveCount(0);

      watch.stop();
      expect(watch.ids.minted).toBe(2);
      expect(watch.ids.deleted).toHaveLength(2);
      expect(watch.ids.deleted[0]).not.toBe(watch.ids.deleted[1]);
      // The id the server called too old is the id the panel settled, unchanged.
      expect(watch.ids.settled).toEqual([watch.ids.deleted[0]]);
    } finally {
      await setClock(admin, 0);
    }
  });

  test("a valid id older than the settle window settles to 'can't tell', never to nothing deleted, and keeps Delete off", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const admin = await apiAs(request, adminName);
    await requireClock(admin);
    const target = await makePerson(admin, `history-${suffix}`, `Hilda Story ${suffix}`);
    try {
      await signInTo(page, adminName, "/admin?tab=users");
      const watch = watchIds(page, target.id);
      const row = await openDelete(page, target);
      await ageFirstDelete(page, target, admin, 61 * 60 + 30);

      await row.getByRole("button", { name: "Delete account" }).click();
      await expect(row.getByText(HISTORY)).toBeVisible();
      await expect(row.getByText(NOT_DELETED)).toHaveCount(0);
      await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
      await expect(row.getByRole("status")).toBeFocused();
      await evidence(page, "history-unavailable");
      watch.stop();
      expect(watch.ids.settled).toEqual(watch.ids.deleted);

      // Close reloads the list. The account is still there, because nothing ran,
      // and once that reload has worked Delete is on again.
      await setClock(admin, 0);
      await row.getByRole("button", { name: "Close" }).click();
      await expect(page.getByRole("row").filter({ hasText: target.email })).toHaveCount(1);
      const again = await openDelete(page, target);
      await expect(again.getByText(HISTORY)).toHaveCount(0);
      await again.getByRole("button", { name: "Delete account" }).click();
      await expect(again.getByText(DELETED)).toBeVisible();
      await again.getByRole("button", { name: "Done" }).click();
      await expect(page.getByRole("row").filter({ hasText: target.email })).toHaveCount(0);
    } finally {
      await setClock(admin, 0);
    }
  });

  test("after 'can't tell', Delete stays off through a failed reload and another person's panel, and returns after a reload that worked", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const admin = await apiAs(request, adminName);
    await requireClock(admin);
    const target = await makePerson(admin, `stay-off-${suffix}`, `Sasha Stay ${suffix}`);
    const bystander = await makePerson(admin, `bystander-${suffix}`, `Bo Bystander ${suffix}`);
    try {
      await signInTo(page, adminName, "/admin?tab=users");
      const watch = watchIds(page, target.id);
      const row = await openDelete(page, target);
      await ageFirstDelete(page, target, admin, 61 * 60 + 30);
      await row.getByRole("button", { name: "Delete account" }).click();
      await expect(row.getByText(HISTORY)).toBeVisible();
      await setClock(admin, 0);

      // The reload that Close asks for fails.
      const usersList = "**/api/v1/admin/users";
      await page.route(usersList, (route) => (route.request().method() === "GET" ? route.abort() : route.fallback()));
      await row.getByRole("button", { name: "Close" }).click();

      // Another person's panel replaces it; that person is not blocked.
      const other = await openDelete(page, bystander);
      await expect(other.getByText(HISTORY)).toHaveCount(0);
      await other.getByRole("button", { name: "Cancel" }).click();

      // Back to the first person: still blocked, and no id is minted for it.
      const mintedBefore = watch.ids.minted;
      const blocked = await openDelete(page, target, false);
      await expect(blocked.getByText(HISTORY)).toBeVisible();
      await expect(blocked.getByRole("button", { name: "Delete account" })).toBeDisabled();
      expect(watch.ids.minted).toBe(mintedBefore);
      await evidence(page, "history-still-off");

      // A reload that works lifts it.
      await page.unroute(usersList);
      await blocked.getByRole("button", { name: "Close" }).click();
      await expect(page.getByRole("row").filter({ hasText: target.email })).toHaveCount(1);
      const again = await openDelete(page, target);
      await expect(again.getByText(HISTORY)).toHaveCount(0);
      await again.getByRole("button", { name: "Delete account" }).click();
      await expect(again.getByText(DELETED)).toBeVisible();
      await again.getByRole("button", { name: "Done" }).click();
      watch.stop();
    } finally {
      await setClock(admin, 0);
      await admin("DELETE", `/admin/users/${bystander.id}`);
    }
  });

  test("a mint answered with a page that is not JSON sends no DELETE and deletes nothing", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const admin = await apiAs(request, adminName);
    const target = await makePerson(admin, `bad-mint-${suffix}`, `Mina Mint ${suffix}`);
    await signInTo(page, adminName, "/admin?tab=users");
    const watch = watchIds(page, target.id);
    const row = await openDelete(page, target);
    await page.route(`**${MINT}`, async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      await route.fulfill({ status: 200, contentType: "text/html", body: "<html>gateway</html>" });
    });

    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByRole("alert")).toBeVisible();
    await expect(row.getByText(DELETED)).toHaveCount(0);
    await expect(row.getByRole("button", { name: "Delete account" })).toBeEnabled();
    watch.stop();
    expect(watch.ids.deleted).toEqual([]);
    expect(watch.ids.settled).toEqual([]);
    await expect(page.getByRole("row").filter({ hasText: target.email })).toHaveCount(1);
    await admin("DELETE", `/admin/users/${target.id}`);
  });

  test("a DELETE answered 200 with a page that is not JSON never says deleted; it settles the same id", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const admin = await apiAs(request, adminName);
    const target = await makePerson(admin, `bad-delete-${suffix}`, `Dara Delete ${suffix}`);
    await signInTo(page, adminName, "/admin?tab=users");
    const watch = watchIds(page, target.id);
    const row = await openDelete(page, target);
    let first = true;
    await page.route(deletePath(target), async (route) => {
      if (route.request().method() !== "DELETE" || !first) return route.fallback();
      first = false;
      // The delete is not run: a gateway page stands in for the answer.
      await route.fulfill({ status: 200, contentType: "text/html", body: "<html>gateway</html>" });
    });

    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByText(NOTHING)).toBeVisible();
    await expect(row.getByText(DELETED)).toHaveCount(0);
    await expect(page.getByRole("row").filter({ hasText: target.email })).toHaveCount(1);
    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByText(DELETED)).toBeVisible();
    await row.getByRole("button", { name: "Done" }).click();
    await expect(page.getByRole("row").filter({ hasText: target.email })).toHaveCount(0);

    watch.stop();
    expect(watch.ids.deleted).toHaveLength(2);
    expect(watch.ids.settled).toEqual([watch.ids.deleted[0]]);
  });

  test("a DELETE answered 400 with a page that is not JSON settles the same id and does not allow a retry before the answer", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const admin = await apiAs(request, adminName);
    const target = await makePerson(admin, `bad-400-${suffix}`, `Bram Four ${suffix}`);
    await signInTo(page, adminName, "/admin?tab=users");
    const watch = watchIds(page, target.id);
    const row = await openDelete(page, target);
    let first = true;
    await page.route(deletePath(target), async (route) => {
      if (route.request().method() !== "DELETE" || !first) return route.fallback();
      first = false;
      await route.fulfill({ status: 400, contentType: "text/html", body: "<html>bad request</html>" });
    });
    const release = await holdSettle(page);

    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByRole("status")).toHaveText(CHECKING);
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    await expect(row.getByRole("alert")).toHaveCount(0);
    release();
    await expect(row.getByText(NOTHING)).toBeVisible();
    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByText(DELETED)).toBeVisible();
    await row.getByRole("button", { name: "Done" }).click();
    await expect(page.getByRole("row").filter({ hasText: target.email })).toHaveCount(0);

    watch.stop();
    expect(watch.ids.deleted).toHaveLength(2);
    expect(watch.ids.settled).toEqual([watch.ids.deleted[0]]);
  });

  test("after restore_detected, Delete stays off through a failed reload and a reopened panel, and returns after a reload that worked", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const admin = await apiAs(request, adminName);
    const target = await makePerson(admin, `restored-${suffix}`, `Rhea Restore ${suffix}`);
    await signInTo(page, adminName, "/admin?tab=users");
    const watch = watchIds(page, target.id);
    const row = await openDelete(page, target);
    let first = true;
    await page.route(deletePath(target), async (route) => {
      if (route.request().method() !== "DELETE" || !first) return route.fallback();
      first = false;
      await route.fulfill({ status: 503, json: { error: "restore", code: "restore_detected" } });
    });

    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByRole("alert")).toHaveText(RESTORE);
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    await evidence(page, "restore-detected");

    const usersList = "**/api/v1/admin/users";
    await page.route(usersList, (route) => (route.request().method() === "GET" ? route.abort() : route.fallback()));
    await row.getByRole("button", { name: "Close" }).click();
    const mintedBefore = watch.ids.minted;
    const blocked = await openDelete(page, target, false);
    await expect(blocked.getByRole("alert")).toHaveText(RESTORE);
    await expect(blocked.getByRole("button", { name: "Delete account" })).toBeDisabled();
    expect(watch.ids.minted).toBe(mintedBefore);

    await page.unroute(usersList);
    await blocked.getByRole("button", { name: "Close" }).click();
    await expect(page.getByRole("row").filter({ hasText: target.email })).toHaveCount(1);
    const again = await openDelete(page, target);
    await expect(again.getByText(RESTORE)).toHaveCount(0);
    await again.getByRole("button", { name: "Delete account" }).click();
    await expect(again.getByText(DELETED)).toBeVisible();
    await again.getByRole("button", { name: "Done" }).click();
    await expect(page.getByRole("row").filter({ hasText: target.email })).toHaveCount(0);
    watch.stop();
  });

  test("a busy answer shows its copy, keeps Delete on, and the next press sends a new id", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const admin = await apiAs(request, adminName);
    const target = await makePerson(admin, `busy-${suffix}`, `Bea Busy ${suffix}`);
    await signInTo(page, adminName, "/admin?tab=users");
    const watch = watchIds(page, target.id);
    const row = await openDelete(page, target);
    await answerFirstDelete(page, target, { status: 409, code: "delete_household_busy" }, false);

    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByRole("alert")).toHaveText(BUSY);
    await expect(row.getByRole("button", { name: "Delete account" })).toBeEnabled();
    await expect(row.getByRole("status")).toHaveCount(0);
    await evidence(page, "busy");

    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByText(DELETED)).toBeVisible();
    await row.getByRole("button", { name: "Done" }).click();
    await expect(page.getByRole("row").filter({ hasText: target.email })).toHaveCount(0);

    watch.stop();
    expect(watch.ids.minted).toBe(2);
    expect(watch.ids.deleted).toHaveLength(2);
    expect(watch.ids.deleted[0]).not.toBe(watch.ids.deleted[1]);
    expect(watch.ids.settled).toEqual([]);
  });
});
