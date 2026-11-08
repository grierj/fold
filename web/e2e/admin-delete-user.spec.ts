import AxeBuilder from "@axe-core/playwright";
import { mkdirSync } from "node:fs";
import { expect, test, type APIRequestContext, type Locator, type Page, type Request, type Route } from "@playwright/test";
import { apiAs, email, fromMore, keepAllReview, nextFrames, PASSWORD, signInTo } from "./helpers";
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
// T07B-2c: the reason review shown before a reasoned account is deleted.
const REVIEW_INTRO = /Before you delete this account, choose for each contact-request reason below/;
const REVIEW_LIMIT = /Fold lists only requests this person made or was asked about/;
const REVIEW_MEMORY = "Your choices stay on this page only until you delete or close it.";
const NOT_ADULT = "Only an adult admin can review these reasons. Ask another admin to delete this account.";
const CHANGED = "Some reasons changed since you loaded them. Check the marked ones, then delete again.";
const MARKED = "Changed since you looked: choose again";
const CANT_RESTORE = "Blanked text can't be brought back in Fold. Backups made before now still contain it.";

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

function stringAt(value: unknown, ...path: string[]): string {
  let at: unknown = value;
  for (const key of path) at = recordOf(at)[key];
  if (typeof at !== "string") throw new Error(`expected a string at ${path.join(".")}`);
  return at;
}

type Asker = Person & { login: string; requestId: number; text: string };

// A person with contact-request reasons: `askers` group leaders each ask them
// for their email, with a reason of their own. A third leader who asks nothing
// keeps the group led when an asker is deleted. The subject hides their contact
// details from leaders, which is what makes a leader ask.
async function makeReasoned(request: APIRequestContext, suffix: string, askers = 2) {
  const admin = await apiAs(request, adminName);
  const subject = await makePerson(admin, `rv-subject-${suffix}`, `Sue Subject ${suffix}`);
  const keeper = await makePerson(admin, `rv-keeper-${suffix}`, `Kit Keeper ${suffix}`);
  const people: Person[] = [];
  for (let i = 1; i <= askers; i++) people.push(await makePerson(admin, `rv-ask${i}-${suffix}`, `Lia ${i} Asker ${suffix}`));
  const { group } = await admin("POST", "/groups", { name: `Review study ${suffix}`, description: "Reason review proof", visibility: "public", tags: [] });
  for (const p of [keeper, ...people]) await admin("POST", `/admin/groups/${numberAt(group, "id")}/members`, { userId: p.id, role: "leader" });
  await admin("POST", `/admin/groups/${numberAt(group, "id")}/members`, { userId: subject.id, role: "member" });
  await (await apiAs(request, `rv-subject-${suffix}`))("PUT", "/me", { hideContactFromLeaders: true });
  const made: Asker[] = [];
  for (const [i, p] of people.entries()) {
    const login = `rv-ask${i + 1}-${suffix}`;
    const text = `Reason ${i + 1} ${suffix}: please share your email so I can organize rides`;
    const res = await (await apiAs(request, login))("POST", "/contact-requests", {
      subjectUserId: subject.id, contextType: "group", contextId: numberAt(group, "id"), fields: "email", reason: text,
    });
    made.push({ ...p, login, requestId: numberAt(res, "request", "id"), text });
  }
  return { subject, askers: made };
}

// A person with many reasons: `askers` leaders each ask them for their email in
// each of `groups` groups (a requester may have 10 open requests, so 12 askers in
// 10 groups give 120 reasons). Returned in the order the review lists them:
// ascending request id.
async function makeManyReasoned(request: APIRequestContext, suffix: string, askers = 12, groups = 10) {
  const admin = await apiAs(request, adminName);
  const subject = await makePerson(admin, `rv-many-subject-${suffix}`, `Mia Many ${suffix}`);
  const people: Person[] = [];
  for (let i = 1; i <= askers; i++) people.push(await makePerson(admin, `rv-many-ask${i}-${suffix}`, `Ash ${i} Many ${suffix}`));
  const groupIds: number[] = [];
  for (let g = 1; g <= groups; g++) {
    const { group } = await admin("POST", "/groups", { name: `Many study ${g} ${suffix}`, description: "Many reasons proof", visibility: "public", tags: [] });
    const id = numberAt(group, "id");
    groupIds.push(id);
    for (const p of people) await admin("POST", `/admin/groups/${id}/members`, { userId: p.id, role: "leader" });
    await admin("POST", `/admin/groups/${id}/members`, { userId: subject.id, role: "member" });
  }
  await (await apiAs(request, `rv-many-subject-${suffix}`))("PUT", "/me", { hideContactFromLeaders: true });
  const made: Asker[] = [];
  for (const [i, p] of people.entries()) {
    const login = `rv-many-ask${i + 1}-${suffix}`;
    const as = await apiAs(request, login);
    for (const [g, groupId] of groupIds.entries()) {
      const text = `Many ${i + 1}-${g + 1} ${suffix}: please share your email so I can organize rides`;
      const res = await as("POST", "/contact-requests", { subjectUserId: subject.id, contextType: "group", contextId: groupId, fields: "email", reason: text });
      made.push({ ...p, login, requestId: numberAt(res, "request", "id"), text });
    }
  }
  made.sort((a, b) => a.requestId - b.requestId);
  return { subject, reasons: made };
}

// The reason a contact request holds now, as the admin reads it.
async function reasonOf(request: APIRequestContext, requestId: number): Promise<string> {
  const admin = await apiAs(request, adminName);
  return stringAt(await admin("GET", `/contact-requests/${requestId}`), "request", "reason");
}

const reasonItem = (row: Locator, text: string) => row.getByRole("list", { name: "Contact-request reasons" }).getByRole("listitem").filter({ hasText: text });

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

test.describe("reviewing contact-request reasons before a delete (T07B-2c)", () => {
  test.use({ locale: "en-US", timezoneId: "America/Boise" });
  test.setTimeout(120_000);

  test("a reasoned person is reviewed item by item and deleted with mixed choices: blanked and kept as chosen", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const { subject, askers } = await makeReasoned(request, suffix);
    const [blankIt, keepIt] = askers;
    await signInTo(page, adminName, "/admin?tab=users");
    const watch = watchIds(page, subject.id);
    const row = await openDelete(page, subject, false);

    // Both reasons are listed, nothing is chosen, nothing offers to choose for all.
    await expect(reasonItem(row, blankIt.text)).toBeVisible();
    await expect(reasonItem(row, keepIt.text)).toBeVisible();
    await expect(row.getByText(REVIEW_INTRO)).toBeVisible();
    await expect(row.getByText(REVIEW_LIMIT)).toBeVisible();
    await expect(row.getByText(REVIEW_MEMORY)).toBeVisible();
    await expect(row.getByRole("radio", { checked: true })).toHaveCount(0);
    await expect(row.getByRole("button", { name: /(keep|blank|choose|select) (all|every)/i })).toHaveCount(0);
    await expect(row.getByText("You're part of this request")).toHaveCount(0);
    await expect(row.getByText("Choose Keep text or Blank text for 2 more reasons.")).toBeVisible();
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    await evidence(page, "reasons-unchosen");

    await reasonItem(row, blankIt.text).getByRole("radio", { name: "Blank text" }).check();
    await expect(row.getByText("Choose Keep text or Blank text for 1 more reason.")).toBeVisible();
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    await reasonItem(row, keepIt.text).getByRole("radio", { name: "Keep text" }).check();
    await expect(row.getByRole("button", { name: "Delete account" })).toBeEnabled();
    await expect(row.getByText("Delete this account. Keep 1 reason and blank 1.")).toBeVisible();
    await expect(row.getByText(CANT_RESTORE)).toBeVisible();
    await evidence(page, "reasons-chosen");

    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByText("The account was deleted. 1 reason was blanked and 1 kept.")).toBeVisible();
    await evidence(page, "reasons-done");
    await row.getByRole("button", { name: "Done" }).click();
    await expect(page.getByRole("row").filter({ hasText: subject.email })).toHaveCount(0);

    watch.stop();
    expect(watch.ids.minted).toBe(1);
    expect(watch.ids.deleted).toHaveLength(1);
    expect(watch.ids.settled).toEqual([]);
    expect(await reasonOf(request, blankIt.requestId)).toBe("");
    expect(await reasonOf(request, keepIt.requestId)).toBe(keepIt.text);
  });

  test("a reason that changes after the page loaded is marked, the other choice stays, and a fresh press deletes", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const { subject, askers } = await makeReasoned(request, suffix);
    const [stays, changes] = askers;
    await signInTo(page, adminName, "/admin?tab=users");
    const watch = watchIds(page, subject.id);
    const row = await openDelete(page, subject, false);
    await reasonItem(row, stays.text).getByRole("radio", { name: "Keep text" }).check();
    await reasonItem(row, changes.text).getByRole("radio", { name: "Blank text" }).check();
    await expect(row.getByRole("button", { name: "Delete account" })).toBeEnabled();

    // The asker withdraws their request, which the server sees as a different row.
    await (await apiAs(request, changes.login))("POST", `/contact-requests/${changes.requestId}/withdraw`, {});
    await row.getByRole("button", { name: "Delete account" }).click();

    await expect(row.getByRole("alert")).toHaveText(CHANGED);
    await expect(row.getByText(MARKED)).toHaveCount(1);
    await expect(reasonItem(row, changes.text).getByText(MARKED)).toBeVisible();
    await expect(reasonItem(row, stays.text).getByRole("radio", { name: "Keep text" })).toBeChecked();
    await expect(reasonItem(row, changes.text).getByRole("radio", { checked: true })).toHaveCount(0);
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    await expect(row.getByText(NOT_DELETED)).toHaveCount(0);
    await evidence(page, "reasons-changed");

    await reasonItem(row, changes.text).getByRole("radio", { name: "Keep text" }).check();
    await expect(row.getByText(MARKED)).toHaveCount(0);
    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByText("The account was deleted. 0 reasons were blanked and 2 kept.")).toBeVisible();
    await row.getByRole("button", { name: "Done" }).click();

    watch.stop();
    expect(watch.ids.minted).toBe(2);
    expect(watch.ids.deleted).toHaveLength(2);
    expect(watch.ids.deleted[0]).not.toBe(watch.ids.deleted[1]);
    expect(watch.ids.settled).toEqual([]);
    expect(await reasonOf(request, stays.requestId)).toBe(stays.text);
    expect(await reasonOf(request, changes.requestId)).toBe(changes.text);
  });

  test("a person with no reasons shows 'No reasons to review' and deletes at once", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const admin = await apiAs(request, adminName);
    const target = await makePerson(admin, `rv-none-${suffix}`, `Nan None ${suffix}`);
    await signInTo(page, adminName, "/admin?tab=users");
    const row = await openDelete(page, target);
    await expect(row.getByText("No reasons to review.")).toBeVisible();
    await expect(row.getByRole("radio")).toHaveCount(0);
    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByText(DELETED, { exact: true })).toBeVisible();
    await expect(row.getByText(/blanked/)).toHaveCount(0);
    await row.getByRole("button", { name: "Done" }).click();
  });

  // The server's 403 for an admin who may not review (not an adult) is answered
  // here by a stub: making a non-adult admin takes an account setup this spec
  // does not need. The copy and the disabled Delete are what is checked.
  test("an admin who may not review reasons is told so, sees no reason text, and cannot delete", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const { subject, askers } = await makeReasoned(request, suffix, 1);
    await page.route(`**/api/v1/admin/users/${subject.id}/erasure-reasons/page`, (route) =>
      route.fulfill({ status: 403, json: { error: "server text the page must not show", code: "reason_review_not_allowed", reviewRequired: true } }));
    await signInTo(page, adminName, "/admin?tab=users");
    const row = await openDelete(page, subject, false);
    await expect(row.getByRole("alert")).toHaveText(NOT_ADULT);
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    await expect(row.getByRole("radio")).toHaveCount(0);
    await expect(row.getByText(askers[0].text)).toHaveCount(0);
    await expect(row.getByText("server text the page must not show")).toHaveCount(0);
    await evidence(page, "reasons-not-allowed");
  });

  // 120 reasons across three display pages, chosen by pointer and by keyboard,
  // moved through with every pager control, changed behind the page's back, and
  // deleted: the real server accepts the browser's count, digest and choices.
  test("120 reasons are chosen by pointer and keyboard over three display pages, survive a concurrent change, and delete", async ({ page, request }) => {
    test.setTimeout(300_000);
    const suffix = Date.now().toString(36);
    const { subject, reasons } = await makeManyReasoned(request, suffix);
    expect(reasons).toHaveLength(120);
    await signInTo(page, adminName, "/admin?tab=users");
    const watch = watchIds(page, subject.id);
    const row = await openDelete(page, subject, false);
    const group = (n: number) => row.getByRole("group", { name: `Reason ${n} of 120` });
    const shown = row.locator("li[data-reason-id]");

    // The whole review is read, but only 50 reasons are on the page.
    await expect(row.getByText("Reasons 1 to 50 of 120", { exact: true })).toBeVisible({ timeout: 60_000 });
    await expect(shown).toHaveCount(50);
    await expect(row.getByText("Not chosen yet")).toHaveCount(50);
    await expect(row.getByRole("radio", { checked: true })).toHaveCount(0);
    await expect(row.getByRole("button", { name: /(keep|blank|choose|select) (all|every)/i })).toHaveCount(0);
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    await expect(row.getByText("Choose Keep text or Blank text for 120 more reasons.")).toBeVisible();
    await evidence(page, "reasons-many-unchosen");

    // Blank every third reason, keep the rest; even positions by pointer, odd by keyboard.
    const blank = (i: number) => i % 3 === 0;
    const choose = async (i: number) => {
      const radio = group(i + 1).getByRole("radio", { name: blank(i) ? "Blank text" : "Keep text" });
      if (i % 2 === 0) await radio.check();
      else await radio.press("Space");
      await expect(radio).toBeChecked();
    };
    for (let i = 0; i < 50; i++) await choose(i);
    await expect(row.getByText("Choose Keep text or Blank text for 70 more reasons.")).toBeVisible();

    // Next 50, then Next reason without a choice (already on page 2), then jump.
    await row.getByRole("button", { name: "Next 50" }).click();
    await expect(row.getByText("Reasons 51 to 100 of 120", { exact: true })).toBeVisible();
    await expect(shown).toHaveCount(50);
    for (let i = 50; i < 100; i++) await choose(i);
    await row.getByRole("button", { name: "Previous 50" }).click();
    await expect(row.getByText("Reasons 1 to 50 of 120", { exact: true })).toBeVisible();
    await expect(group(8).getByRole("radio", { name: "Keep text" })).toBeChecked(); // position 7 is not a multiple of 3
    await expect(group(4).getByRole("radio", { name: "Blank text" })).toBeChecked(); // position 3 is
    await row.getByRole("button", { name: "Next reason without a choice" }).click();
    await expect(row.getByText("Reasons 101 to 120 of 120", { exact: true })).toBeVisible();
    await expect(page.locator('[data-reason-heading="100"]')).toBeFocused();
    await row.getByLabel("Go to reason number").fill("1");
    await row.getByRole("button", { name: "Go" }).click();
    await expect(page.locator('[data-reason-heading="0"]')).toBeFocused();
    await row.getByLabel("Go to reason number").fill("120");
    await row.getByRole("button", { name: "Go" }).click();
    await expect(shown).toHaveCount(20);
    await expect(page.locator('[data-reason-heading="119"]')).toBeFocused();
    for (let i = 100; i < 119; i++) await choose(i);
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    await expect(row.getByText("Choose Keep text or Blank text for 1 more reason.")).toBeVisible();
    await choose(119);
    await expect(row.getByRole("button", { name: "Next reason without a choice" })).toBeDisabled();
    await expect(row.getByRole("button", { name: "Delete account" })).toBeEnabled();
    await expect(row.getByText("Delete this account. Keep 80 reasons and blank 40.")).toBeVisible();
    await evidence(page, "reasons-many-chosen");

    // A reason changes behind the page: the asker of reason 6 withdraws.
    const changed = reasons[5];
    await (await apiAs(request, changed.login))("POST", `/contact-requests/${changed.requestId}/withdraw`, {});
    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByRole("alert")).toHaveText(CHANGED);
    // Everything else stays chosen; the changed one needs a choice, and the pager finds it.
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    await expect(row.getByText("Choose Keep text or Blank text for 1 more reason.")).toBeVisible({ timeout: 60_000 });
    await expect(page.locator('[data-reason-heading="5"]')).toBeFocused();
    await expect(row.getByText(MARKED)).toHaveCount(1);
    await expect(group(6).getByRole("radio", { checked: true })).toHaveCount(0);
    await expect(group(8).getByRole("radio", { name: "Keep text" })).toBeChecked();
    await row.getByRole("button", { name: "Next 50" }).click();
    await expect(group(51).getByRole("radio", { name: blank(50) ? "Blank text" : "Keep text" })).toBeChecked();
    await row.getByRole("button", { name: "Next changed reason" }).click();
    await expect(page.locator('[data-reason-heading="5"]')).toBeFocused();
    await choose(5);
    await expect(row.getByRole("button", { name: "Next changed reason" })).toHaveCount(0);
    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByText("The account was deleted. 40 reasons were blanked and 80 kept.")).toBeVisible();
    await row.getByRole("button", { name: "Done" }).click();

    watch.stop();
    expect(watch.ids.minted).toBe(2);
    expect(watch.ids.deleted).toHaveLength(2);
    expect(watch.ids.settled).toEqual([]);
    for (const i of [0, 1, 2, 3, 50, 60, 99, 100, 118, 119]) {
      expect(await reasonOf(request, reasons[i].requestId), `reason ${i + 1}`).toBe(blank(i) ? "" : reasons[i].text);
    }
  });

  // The browser has no crypto.subtle on a plain-http address (the init script
  // removes it, as such a page lacks it): the bundled SHA-256 makes the digest,
  // and the real server accepts it and deletes.
  test("with no crypto.subtle the bundled SHA-256 makes a digest the server accepts", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const { subject, askers } = await makeReasoned(request, suffix);
    const [blankIt, keepIt] = askers;
    await page.addInitScript(() => {
      Object.defineProperty(window.crypto, "subtle", { value: undefined, configurable: true });
    });
    await signInTo(page, adminName, "/admin?tab=users");
    expect(await page.evaluate(() => window.crypto.subtle === undefined)).toBe(true);
    const row = await openDelete(page, subject, false);
    await reasonItem(row, blankIt.text).getByRole("radio", { name: "Blank text" }).check();
    await reasonItem(row, keepIt.text).getByRole("radio", { name: "Keep text" }).check();
    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByText("The account was deleted. 1 reason was blanked and 1 kept.")).toBeVisible();
    expect(await reasonOf(request, blankIt.requestId)).toBe("");
    expect(await reasonOf(request, keepIt.requestId)).toBe(keepIt.text);
  });

  // The same refusal the page route gives, met at the DELETE instead (an admin
  // who stopped being allowed to review after the pages were read). A real
  // under-18 admin cannot be made through the API (a teen account cannot be an
  // admin and the adult band needs an adult birth date), so the answer is
  // stubbed; the server's own order of refusals is pinned by the Go matrix.
  test("a delete refused as not allowed after the choices were made says so, stays off and settles nothing", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const { subject, askers } = await makeReasoned(request, suffix, 1);
    await page.route(deletePath(subject), async (route: Route) => {
      if (route.request().method() !== "DELETE") return route.fallback();
      await route.fulfill({ status: 403, json: { error: "server text the page must not show", code: "reason_review_not_allowed", reviewRequired: true } });
    });
    await signInTo(page, adminName, "/admin?tab=users");
    const watch = watchIds(page, subject.id);
    const row = await openDelete(page, subject, false);
    await reasonItem(row, askers[0].text).getByRole("radio", { name: "Keep text" }).check();
    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByRole("alert")).toHaveText(NOT_ADULT);
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    await expect(row.getByText("server text the page must not show")).toHaveCount(0);
    await expect(row.getByText(NOT_DELETED)).toHaveCount(0);
    watch.stop();
    expect(watch.ids.minted).toBe(1);
    expect(watch.ids.settled).toEqual([]);
    expect(await reasonOf(request, askers[0].requestId)).toBe(askers[0].text);
  });

  // A page from before this version sends {items: [...]}: the server refuses it
  // with reason_review_outdated and applies nothing, whatever the hashes say.
  test("an old-shape review is refused as outdated and deletes nothing", async ({ request }) => {
    const suffix = Date.now().toString(36);
    const { subject, askers } = await makeReasoned(request, suffix, 1);
    const admin = await apiAs(request, adminName);
    const first = await admin("POST", `/admin/users/${subject.id}/erasure-reasons/page`, { after: null });
    const items = (recordOf(first).items as unknown[]).map((it) => ({ requestId: stringAt(it, "requestId"), reviewHash: stringAt(it, "reviewHash"), choice: "remove" }));
    const csrf = ((await (await request.get("/api/v1/auth/csrf")).json()) as { csrfToken: string }).csrfToken;
    const res = await request.fetch(`/api/v1/admin/users/${subject.id}`, {
      method: "DELETE", data: { successors: {}, hostSuccessors: {}, reasonReview: { items } }, headers: { "X-CSRF-Token": csrf },
    });
    expect(res.status()).toBe(409);
    expect(((await res.json()) as { code: string }).code).toBe("reason_review_outdated");
    expect(await reasonOf(request, askers[0].requestId)).toBe(askers[0].text);
  });

  test("two deleted askers are told apart by reference, never named", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const { subject, askers } = await makeReasoned(request, suffix);
    // Delete both askers first (each is the requester of a reason, so each
    // delete carries a review of its own); their requests about the subject stay.
    const admin = await apiAs(request, adminName);
    for (const asker of askers) {
      const first = await admin("POST", `/admin/users/${asker.id}/erasure-reasons/page`, { after: null });
      const items = (recordOf(first).items as unknown[]).map((it) => ({
        requestId: stringAt(it, "requestId"), reviewHash: stringAt(it, "reviewHash"),
      }));
      await admin("DELETE", `/admin/users/${asker.id}`, { successors: {}, hostSuccessors: {}, reasonReview: keepAllReview(items) });
    }
    await signInTo(page, adminName, "/admin?tab=users");
    const row = await openDelete(page, subject, false);
    // The panel is open before its review pages have loaded (expectReady is
    // false), and allTextContents does not wait: wait for both labels first.
    const labels = row.getByText(/Deleted account · ref \d+/);
    await expect(labels).toHaveCount(2);
    const refs = await labels.allTextContents();
    const ids = refs.map((t) => /ref (\d+)/.exec(t)?.[1]);
    expect(ids[0]).toBeDefined();
    expect(ids[0]).not.toBe(ids[1]);
    for (const asker of askers) await expect(row.getByText(asker.name)).toHaveCount(0);
    await evidence(page, "reasons-deleted-counterparts");
  });
});
