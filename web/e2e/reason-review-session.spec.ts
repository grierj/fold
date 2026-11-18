import { isString } from "../src/lib/wireValues";
import { expect, test, type APIRequestContext, type Locator, type Page, type Request, type Route } from "@playwright/test";
import { apiAs, checkState, fromMore, PASSWORD, signInTo } from "./helpers";
import { decideElsewhere, makeMany, makeLegacy, makePerson, pageOf, recordOf, stringAt, type Many } from "./legacy-reasons";
import { partSelected } from "./ui-pages";

// Long reason reviews, used the way a person at a phone would (#295): a long
// history that takes several pages, the keyboard alone, a screen reader's view
// (names and live regions), a connection that drops mid-review, and a session
// that is closed, reloaded, changed under the admin, or runs out. Both reviews
// are covered: the deleted-accounts page (the legacy review, pages of 100) and
// the delete panel (the walk reads pages of 100 and shows 50 at a time).
//
// What a review keeps is decided by lib/reasonReviewStore.ts and the carry in
// pages/ReasonReviewLegacy.tsx: the choices live in the page's memory only. A
// reload that follows a changed set or a save that did not run keeps a choice
// only where the reason's hash is unchanged; closing, leaving, reloading the
// browser or signing in again keeps nothing. Each test says what it saw with
// `kept` and `lost`, which put it in the report (annotations "retention"), and
// the table in docs/plan/M7.4-ERASURE-INVENTORY.md 12.3 is read from them.
// Time is reported the same way: "observed" is what this run measured with the
// test binary's clock shifted, "assumed" is what the server's constants say
// (10 minutes to run, 61 minutes of memory) taken at face value.
//
// The people and groups are made through the API, so this spec depends on no
// other spec and no seed user besides the admin. Every test runs whole at 390
// and again at 1280 px (the viewport is a test option, so a reload, a sign-in
// or an ended session keeps it): the keys, the answers, the recovery steps and
// each state's axe and layout check (checkState) all happen at that width.

const adminName = "grier";
const MINT = "/api/v1/admin/operation-attempts";
const SETTLE = "/api/v1/admin/operation-attempts/settle";
const LEGACY = "/api/v1/admin/erasure-reasons/legacy";

const TITLE = "Deleted accounts with reasons to review";
const SAVED = "Saved.";
const ACCOUNT_EMPTY = "This account has no reasons left to review.";
const STILL = "Still checking. Fold can't tell yet whether your choices were saved. Check again in a moment.";
const NOT_APPLIED = "These choices weren't saved. Reload to see what's left.";
const HISTORY = "Fold can't tell whether your earlier save finished. Reload to see what's left.";
const CHANGED = "Some reasons changed since you loaded them. Check the marked ones, then save again.";
const NOT_SAVED = /weren't saved|not saved|wasn't saved|was not saved/i;
const SIGNED_OUT = /signed out|sign in/i;

const DELETED = "The account was deleted.";
const DEL_STILL = "Still checking. Fold can't tell yet whether the account was deleted. Check again in a moment.";
const DEL_NOTHING = "Nothing was deleted by this attempt.";
const DEL_CHANGED = "Some reasons changed since you loaded them. Check the marked ones, then delete again.";
const DEL_HISTORY = "Fold can't tell whether an earlier delete finished. Reload this person's page to check.";
const DEL_RESTORE = "Fold noticed its database was restored while it was running, so nothing was changed. It is restarting; try again in a minute.";
const DEL_NOT_DELETED = /nothing was deleted|not deleted|wasn't deleted|was not deleted/i;
const WALK_FAILED = "Fold couldn't load the reasons just now. Try again in a minute.";
const MARKED = "Changed since you looked: choose again";

type Pick = "keep" | "remove";

// A fixed mix, the same for every test: every third reason is blanked.
const pickOf = (i: number): Pick => (i % 3 === 0 ? "remove" : "keep");

// What a review kept or lost, and how long things took, for the report.
function kept(event: string, surface: string, detail: string) {
  test.info().annotations.push({ type: "retention", description: `${event} | ${surface} | kept | ${detail}` });
}

function lost(event: string, surface: string, detail: string) {
  test.info().annotations.push({ type: "retention", description: `${event} | ${surface} | lost | ${detail}` });
}

function observed(label: string, ms: number) {
  test.info().annotations.push({ type: "observed-time", description: `${label} | ${Math.round(ms)} ms` });
}

async function timed<T>(label: string, run: () => Promise<T>): Promise<T> {
  const at = Date.now();
  const out = await run();
  observed(label, Date.now() - at);

  return out;
}

// ---- the deleted-accounts page ----

const reasonList = (page: Page) => page.getByRole("list", { name: "Contact-request reasons" });
const reasonItems = (page: Page) => page.locator("li[data-reason-id]");
const checkedRadios = (page: Page) => page.locator("li[data-reason-id] input[type=radio]:checked");
const saveButton = (page: Page) => page.getByRole("button", { name: "Save", exact: true });
const accountPath = (id: string) => `/admin/deleted-reasons?account=${id}`;
const savedStatus = (page: Page) => page.getByRole("status").filter({ hasText: SAVED });

// The element holding focus, as the words it shows (the message box after an answer).
const focusedText = (page: Page): Promise<string> => page.evaluate(() => document.activeElement?.textContent ?? "");

// Chooses for every reason on the page in one step, by clicking each row's radio:
// the same event a pointer makes, without a hundred round trips.
async function chooseAll(page: Page, pick: (i: number) => Pick = pickOf) {
  const n = await reasonItems(page).count();
  const picks = Array.from({ length: n }, (_, i) => pick(i));
  await page.evaluate((choices) => {
    document.querySelectorAll<HTMLElement>("li[data-reason-id]").forEach((li, i) => {
      li.querySelector<HTMLInputElement>(`input[type=radio][value=${choices[i]}]`)?.click();
    });
  }, picks);
  await expect(checkedRadios(page)).toHaveCount(n);
}

// Focus is on a group's first radio: Space keeps, Down blanks, Tab moves to the next group.
async function chooseWithKeys(page: Page, n: number, pick: (i: number) => Pick = pickOf) {
  for (let i = 0; i < n; i++) {
    await page.keyboard.press(pick(i) === "remove" ? "ArrowDown" : "Space");
    await page.keyboard.press("Tab");
  }
}

// The requests the legacy page sends: ids minted, what each PUT and settle carried,
// and the page reads. An id is a capability, so it is compared and never printed.
function watchSave(page: Page, id: string) {
  const put: string[] = [];
  const settled: string[] = [];
  const seen = { minted: 0, put, settled, pages: 0 };

  const onRequest = (r: Request) => {
    const path = new URL(r.url()).pathname;
    const body = r.postData() ? recordOf(JSON.parse(r.postData() ?? "{}")) : {};

    if (r.method() === "POST" && path === MINT && body.kind === "legacy_reason_save") seen.minted++;

    if (r.method() === "PUT" && path === `${LEGACY}/${id}` && isString(body.attemptId)) seen.put.push(body.attemptId);

    if (r.method() === "POST" && path === SETTLE && isString(body.attemptId)) seen.settled.push(body.attemptId);

    if (r.method() === "POST" && path === `${LEGACY}/${id}/page`) seen.pages++;
  };

  page.on("request", onRequest);

  return { seen, stop: () => page.off("request", onRequest) };
}

// Aborts the first PUT for this account before it leaves, as a dropped connection does.
async function dropFirstPut(page: Page, id: string) {
  let first = true;
  await page.route(`**${LEGACY}/${id}`, async (route: Route) => {
    if (route.request().method() !== "PUT" || !first) return route.fallback();
    first = false;
    await route.abort("failed");
  });
}

// Aborts the first settle call, so the answer to "what happened" is itself lost.
async function dropFirstSettle(page: Page) {
  let first = true;
  await page.route(`**${SETTLE}`, async (route: Route) => {
    if (!first) return route.fallback();
    first = false;
    await route.abort("failed");
  });
}

// ---- the delete panel ----

const deletePanel = (page: Page, target: { email: string }) => page.getByRole("row").filter({ hasText: target.email });
const panelItems = (row: Locator) => row.locator("li[data-reason-id]");

async function openDeletePanel(page: Page, target: { email: string }, reasons: number): Promise<Locator> {
  const shown = Math.min(50, reasons);
  await page.getByRole("searchbox", { name: "Search by name or email" }).fill(target.email);
  const row = deletePanel(page, target);
  await fromMore(row, "Delete");
  await expect(row.getByText(`Reasons 1 to ${shown} of ${reasons}`, { exact: true })).toBeVisible({ timeout: 60_000 });

  return row;
}

function watchDelete(page: Page, id: number) {
  const deleted: string[] = [];
  const settled: string[] = [];
  const seen = { minted: 0, deleted, settled, pages: 0 };

  const onRequest = (r: Request) => {
    const path = new URL(r.url()).pathname;
    const body = r.postData() ? recordOf(JSON.parse(r.postData() ?? "{}")) : {};

    if (r.method() === "POST" && path === MINT) seen.minted++;

    if (r.method() === "DELETE" && path === `/api/v1/admin/users/${id}` && isString(body.attemptId)) seen.deleted.push(body.attemptId);

    if (r.method() === "POST" && path === SETTLE && isString(body.attemptId)) seen.settled.push(body.attemptId);

    if (r.method() === "POST" && path === `/api/v1/admin/users/${id}/erasure-reasons/page`) seen.pages++;
  };

  page.on("request", onRequest);

  return { seen, stop: () => page.off("request", onRequest) };
}

// The chosen mix on the display page the panel shows (50 at a time), by keyboard.
async function chooseDisplayPageWithKeys(row: Locator, page: Page, first: number, count: number) {
  await row.locator(`[data-reason-heading="${first}"]`).focus();
  await page.keyboard.press("Tab");

  for (let i = 0; i < count; i++) {
    await page.keyboard.press(pickOf(first + i) === "remove" ? "ArrowDown" : "Space");
    await page.keyboard.press("Tab");
  }
}

async function setClock(request: APIRequestContext, offsetSeconds: number) {
  // Signed in afresh each time: another call to apiAs on this context replaces the CSRF token a held client carries.
  const admin = await apiAs(request, adminName);
  await admin("POST", "/e2e/attempt-clock", { offsetSeconds });
}

// Each width is its own part ("reason-review:390"), so e2e/run.sh can put the two on different shards.
for (const width of [390, 1280]) if (partSelected("reason-review", width)) test.describe(`long reason reviews at ${width}px: keyboard, assistive technology, lost connections and ended sessions (#295)`, () => {
  test.use({ locale: "en-US", timezoneId: "America/Boise", viewport: { width, height: 900 } });
  test.setTimeout(600_000);

  test("the deleted-accounts page is chosen and saved page after page by keyboard alone, at phone width, and every control and answer is named", async ({ page, request }) => {
    const suffix = `${Date.now().toString(36)}${width}`;
    // 216 reasons make three pages of 100, 100 and 16.
    const fx = await timed("fixture: 216 deleted-account reasons", () => makeMany(request, `${suffix}k`, `Kay Keys ${suffix}`, { askers: 12, groups: 18, withdraw: true, deleted: true }));
    expect(fx.reasons).toHaveLength(216);
    await signInTo(page, adminName, accountPath(fx.id));
    await timed("first page of 100 shown", async () => {
      await expect(reasonItems(page)).toHaveCount(100, { timeout: 30_000 });
      await expect(page.getByText("Checking…", { exact: true })).toHaveCount(0);
    });

    // Names: the page, the list, each group of two choices, each choice, the one action.
    await expect(page.getByRole("heading", { name: `Deleted account · ref ${fx.id}`, level: 1, exact: true })).toBeVisible();
    await expect(reasonList(page)).toBeVisible();
    await expect(page.getByRole("group", { name: /^Reason \d+ of 100$/ })).toHaveCount(100);
    await expect(page.getByRole("group", { name: "Reason 1 of 100" }).getByRole("radio")).toHaveCount(2);
    await expect(page.getByRole("group", { name: "Reason 100 of 100" }).getByRole("radio", { name: "Blank text" })).toBeVisible();
    await expect(page.getByRole("radio", { name: "Keep text" })).toHaveCount(100);
    await expect(page.getByRole("radio", { name: "Blank text" })).toHaveCount(100);
    await expect(saveButton(page)).toHaveAccessibleName("Save");
    await expect(saveButton(page)).toBeDisabled();
    await expect(page.getByText("Choose Keep text or Blank text for 100 more reasons.")).toBeVisible();
    // Live regions are role=status (polite); nothing says "Saved." yet.
    await expect(savedStatus(page)).toHaveCount(0);
    expect(await page.locator("[aria-live=assertive], [role=alert]").count()).toBe(0);
    await checkState(page, "reason-review-session", "legacy-page-1-unchosen");

    // The first Tab from the link back to the list lands on the first reason's first choice.
    await page.getByRole("link", { name: TITLE }).focus();
    await page.keyboard.press("Tab");
    await expect(page.getByRole("group", { name: "Reason 1 of 100" }).getByRole("radio", { name: "Keep text" })).toBeFocused();
    // 99 choices, then the last one by hand so the Save button is seen going from off to on.
    await timed("100 choices by keyboard (page 1)", () => chooseWithKeys(page, 99));
    await expect(page.getByRole("group", { name: "Reason 100 of 100" }).getByRole("radio", { name: "Keep text" })).toBeFocused();
    await expect(saveButton(page)).toBeDisabled();
    await expect(page.getByText("Choose Keep text or Blank text for 1 more reason.")).toBeVisible();
    await page.keyboard.press("Space");
    await expect(saveButton(page)).toBeEnabled();
    await expect(page.getByText("Saving blanks the text you chose right away.")).toBeVisible();
    await page.keyboard.press("Tab");
    await expect(saveButton(page)).toBeFocused();
    await checkState(page, "reason-review-session", "legacy-page-1-chosen");

    const watch = watchSave(page, fx.id);
    await page.keyboard.press("Enter");
    await expect(savedStatus(page)).toBeVisible();
    // Focus lands on the answer, because the button that was pressed is off.
    await expect.poll(() => focusedText(page)).toContain(SAVED);
    // The next page of the same account is on screen and unchosen: nothing carried over.
    await expect(reasonItems(page)).toHaveCount(100);
    await expect(checkedRadios(page)).toHaveCount(0);
    await expect(saveButton(page)).toBeDisabled();
    expect(await reasonItems(page).first().getAttribute("data-reason-id")).toBe(String(fx.reasons[100].requestId));

    // Page 2 by keyboard from where focus is; Tab from the message box enters the first group.
    await page.keyboard.press("Tab");
    await expect(page.getByRole("group", { name: "Reason 1 of 100" }).getByRole("radio", { name: "Keep text" })).toBeFocused();
    await chooseWithKeys(page, 100, (i) => pickOf(i + 1));
    await expect(saveButton(page)).toBeFocused();
    await page.keyboard.press("Space");
    await expect(savedStatus(page)).toBeVisible();
    await expect.poll(() => focusedText(page)).toContain(SAVED);

    // Page 3: the 16 left. The group names count what is on this page.
    await expect(reasonItems(page)).toHaveCount(16);
    await expect(page.getByRole("group", { name: /^Reason \d+ of 16$/ })).toHaveCount(16);
    await page.keyboard.press("Tab");
    await chooseWithKeys(page, 16, (i) => (i % 2 === 0 ? "keep" : "remove"));
    await expect(saveButton(page)).toBeFocused();
    await checkState(page, "reason-review-session", "legacy-page-3-chosen");
    await page.keyboard.press("Enter");
    await expect(savedStatus(page)).toBeVisible();
    await expect(page.getByText(ACCOUNT_EMPTY)).toBeVisible();
    await expect(saveButton(page)).toHaveCount(0);
    await expect.poll(() => focusedText(page)).toContain(SAVED);
    await checkState(page, "reason-review-session", "legacy-saved-empty");
    watch.stop();
    expect(watch.seen.minted).toBe(3);
    expect(watch.seen.put).toHaveLength(3);
    expect(new Set(watch.seen.put).size).toBe(3);
    expect(watch.seen.settled).toEqual([]);
    expect(await pageOf(await apiAs(request, adminName), fx.id)).toEqual([]);

    // The server holds what the keys chose, on the first and the last reason of each page.
    const admin = await apiAs(request, adminName);
    const reasonOf = async (r: Many["reasons"][number]) => stringAt(await admin("GET", `/contact-requests/${r.requestId}`), "request", "reason");

    for (const [index, choice] of [[0, pickOf(0)], [98, pickOf(98)], [99, "keep"], [100, pickOf(1)], [199, pickOf(100)], [200, "keep"], [201, "remove"], [215, "remove"]] as const) {
      const r = fx.reasons[index];
      expect(await reasonOf(r), `reason ${index + 1}`).toBe(choice === "remove" ? "" : r.text);
    }

    kept("save, three pages in turn", "deleted-accounts page", "each Save sends one page of at most 100; the next page starts unchosen");
  });

  test("the delete panel is reviewed, paged and deleted by keyboard alone; counts and moves are announced politely and focus never falls off", async ({ page, request }) => {
    const suffix = `${Date.now().toString(36)}${width}`;
    // 110 reasons: the walk reads 100 and then 10, and the panel shows 50, 50 and 10.
    const fx = await timed("fixture: 110 delete-panel reasons", () => makeMany(request, `${suffix}d`, `Dee Delete ${suffix}`, { askers: 11, groups: 10, withdraw: false, deleted: false }));
    const subject = { id: Number(fx.id), email: fx.subjectEmail };
    await signInTo(page, adminName, "/admin?tab=users");
    const watch = watchDelete(page, subject.id);
    const row = await timed("walk of 110 reasons shown", () => openDeletePanel(page, subject, 110));
    await expect(panelItems(row)).toHaveCount(50);
    expect(watch.seen.pages).toBe(2);

    // Names and live regions: the review is a labelled region, the pager a labelled nav,
    // each reason a labelled group, and the announcements are polite status text.
    await expect(row.getByRole("region", { name: "Reasons to review" })).toBeVisible();
    await expect(row.getByRole("navigation", { name: "Reasons pages" })).toBeVisible();
    await expect(row.getByRole("group", { name: /^Reason \d+ of 110$/ })).toHaveCount(50);
    await expect(row.getByRole("group", { name: "Reason 50 of 110" }).getByRole("radio")).toHaveCount(2);
    await expect(row.getByLabel("Go to reason number")).toHaveAccessibleName("Go to reason number");
    const live = row.locator("p[role=status][aria-live=polite]");
    await expect(live).toHaveCount(2);
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    await checkState(page, "reason-review-session", "panel-unchosen");

    // Two polite lines: the progress count (first) and where the pager has moved to (second). Choosing is
    // not announced one by one: the count is said after the admin pauses for two seconds, never per click.
    const progress = live.nth(0);
    const moves = live.nth(1);
    await expect(progress).toHaveText("Choose Keep text or Blank text for 110 more reasons.", { timeout: 6_000 });
    await expect(moves).toHaveText("");
    await row.locator('[data-reason-heading="0"]').focus();
    await page.keyboard.press("Tab");
    await page.keyboard.press("Space");
    await page.keyboard.press("Tab");
    await page.keyboard.press("Space");
    await expect(row.getByText("Choose Keep text or Blank text for 108 more reasons.").first()).toBeVisible();
    await expect(progress).toHaveText("Choose Keep text or Blank text for 110 more reasons.");
    await expect(progress).toHaveText("Choose Keep text or Blank text for 108 more reasons.", { timeout: 6_000 });

    // The first display page: heading 0 has focus, Tab enters the first group, each group by keys.
    await timed("50 choices by keyboard (display page 1)", async () => {
      await chooseDisplayPageWithKeys(row, page, 0, 1);
      await row.locator('[data-reason-heading="1"]').focus();
      await page.keyboard.press("Tab");
      await chooseWithKeys(page, 49, (i) => pickOf(i + 1));
    });
    await expect(row.getByText("Choose Keep text or Blank text for 60 more reasons.").first()).toBeVisible();

    // Next 50 by keyboard: Enter on the button, and focus follows to the reason it shows.
    await row.getByRole("button", { name: "Next 50" }).focus();
    await page.keyboard.press("Enter");
    await expect(row.getByText("Reasons 51 to 100 of 110", { exact: true })).toBeVisible();
    await expect(row.locator('[data-reason-heading="50"]')).toBeFocused();
    await expect(row.getByText("Showing reasons 51 to 100 of 110. Reason 51 needs a choice.")).toBeAttached();
    await page.keyboard.press("Tab");
    await chooseWithKeys(page, 50, (i) => pickOf(i + 50));
    await expect(row.getByText("Choose Keep text or Blank text for 10 more reasons.").first()).toBeVisible();

    // Next reason without a choice, by keyboard: lands on the heading of reason 101.
    await row.getByRole("button", { name: "Next reason without a choice" }).focus();
    await page.keyboard.press("Enter");
    await expect(row.getByText("Reasons 101 to 110 of 110", { exact: true })).toBeVisible();
    await expect(row.locator('[data-reason-heading="100"]')).toBeFocused();
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    await page.keyboard.press("Tab");
    await chooseWithKeys(page, 9, (i) => pickOf(i + 100));
    await expect(row.getByRole("group", { name: "Reason 110 of 110" }).getByRole("radio", { name: "Keep text" })).toBeFocused();
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    await page.keyboard.press(pickOf(109) === "remove" ? "ArrowDown" : "Space");
    await expect(row.getByRole("button", { name: "Delete account" })).toBeEnabled();
    const blanked = Array.from({ length: 110 }, (_, i) => pickOf(i)).filter((p) => p === "remove").length;
    await expect(row.getByText(`Delete this account. Keep ${110 - blanked} reasons and blank ${blanked}.`)).toBeVisible();
    await expect(row.getByRole("button", { name: "Next reason without a choice" })).toBeDisabled();
    await checkState(page, "reason-review-session", "panel-chosen");

    // Delete by keyboard. Focus lands on the answer when the button it was on goes away.
    await row.getByRole("button", { name: "Delete account" }).focus();
    await page.keyboard.press("Enter");
    await expect(row.getByText(`${DELETED} ${blanked} reasons were blanked and ${110 - blanked} kept.`)).toBeVisible();
    await expect(row.getByRole("status").filter({ hasText: DELETED })).toBeFocused();
    await checkState(page, "reason-review-session", "panel-done");
    await row.getByRole("button", { name: "Done" }).focus();
    await page.keyboard.press("Enter");
    await expect(deletePanel(page, subject)).toHaveCount(0);
    watch.stop();
    expect(watch.seen.minted).toBe(1);
    expect(watch.seen.deleted).toHaveLength(1);
    expect(watch.seen.settled).toEqual([]);

    const admin = await apiAs(request, adminName);

    for (const i of [0, 1, 3, 49, 50, 99, 100, 108, 109]) {
      const r = fx.reasons[i];
      const got = stringAt(await admin("GET", `/contact-requests/${r.requestId}`), "request", "reason");
      expect(got, `reason ${i + 1}`).toBe(pickOf(i) === "remove" ? "" : r.text);
    }

    kept("delete, 110 reasons by keyboard", "delete panel", "every choice reached the server on one DELETE");
  });

  test("a save that drops, a settle that drops, a changed set, a leave and a reload: what the deleted-accounts page keeps at a hundred reasons", async ({ page, request }) => {
    const suffix = `${Date.now().toString(36)}${width}`;
    // 102 reasons: a page of 100, then 2.
    const fx = await timed("fixture: 102 deleted-account reasons", () => makeMany(request, `${suffix}i`, `Ivy Interrupted ${suffix}`, { askers: 6, groups: 17, withdraw: true, deleted: true }));
    expect(fx.reasons).toHaveLength(102);
    await signInTo(page, adminName, accountPath(fx.id));
    await expect(reasonItems(page)).toHaveCount(100, { timeout: 30_000 });
    await chooseAll(page);
    await expect(saveButton(page)).toBeEnabled();
    const watch = watchSave(page, fx.id);

    // 1. The PUT never leaves and the first settle is lost too: no "not saved" until a settle says so.
    await dropFirstPut(page, fx.id);
    await dropFirstSettle(page);
    const savePressedAt = Date.now();
    await saveButton(page).click();
    const still = page.getByRole("status").filter({ hasText: "Still checking" });
    await expect(still).toHaveText(STILL);
    await expect(page.getByText(NOT_SAVED)).toHaveCount(0);
    await expect(saveButton(page)).toBeDisabled();
    await expect(page.getByRole("button", { name: "Check again" })).toBeVisible();
    await expect(page.getByRole("radio", { name: "Keep text" }).first()).toBeDisabled();
    await expect.poll(() => focusedText(page)).toContain("Still checking");
    observed("a lost save and a lost settle reach 'Still checking'", Date.now() - savePressedAt);
    await checkState(page, "reason-review-session", "legacy-unknown-outcome");
    expect(watch.seen.minted).toBe(1);
    expect(watch.seen.put).toHaveLength(1);

    // Check again settles the same id; the PUT never ran, so the page says so and keeps every choice.
    await page.getByRole("button", { name: "Check again" }).click();
    await expect(page.getByText(NOT_APPLIED)).toBeVisible();
    await expect(page.getByText(NOT_SAVED)).toHaveCount(1);
    expect(watch.seen.settled).toHaveLength(2);
    expect(watch.seen.settled[0]).toBe(watch.seen.put[0]);
    expect(watch.seen.settled[1]).toBe(watch.seen.put[0]);
    await expect(reasonItems(page)).toHaveCount(100);
    await expect(checkedRadios(page)).toHaveCount(100);
    await expect(saveButton(page)).toBeEnabled();
    kept("a save that did not run (lost PUT, then settled not applied)", "deleted-accounts page", "all 100 choices kept; hashes unchanged");

    // 2. Another admin decides one reason before the next press: the save answers changed.
    const decided = fx.reasons[3];
    await decideElsewhere(request, fx.id, decided.text, "keep");
    await saveButton(page).click();
    await expect(page.getByRole("alert")).toContainText(CHANGED);
    await expect(reasonItem(page, decided.text)).toHaveCount(0);
    // The page is read again: the 99 untouched reasons keep their choice, and the one that came up
    // from the next page (the 101st) is new, marked and unchosen.
    await expect(reasonItems(page)).toHaveCount(100);
    await expect(checkedRadios(page)).toHaveCount(99);
    await expect(page.getByText(MARKED)).toHaveCount(1);
    await expect(reasonItem(page, fx.reasons[100].text).getByText(MARKED)).toBeVisible();
    await expect(saveButton(page)).toBeDisabled();
    kept("a changed set (409): another admin decided one reason", "deleted-accounts page", "99 of 100 kept; the new one marked and unchosen");
    lost("a changed set (409): another admin decided one reason", "deleted-accounts page", "the decided reason's choice is gone with it");
    await checkState(page, "reason-review-session", "legacy-changed-at-100");

    // 3. Leaving the account and coming back keeps nothing; neither does reloading the browser.
    await reasonItem(page, fx.reasons[100].text).getByRole("radio", { name: "Keep text" }).check();
    await expect(saveButton(page)).toBeEnabled();
    await page.getByRole("link", { name: TITLE }).click();
    await expect(page.getByRole("heading", { name: TITLE, level: 1 })).toBeVisible();
    await page.getByRole("list", { name: "Deleted accounts" }).getByRole("link", { name: `Deleted account · ref ${fx.id}`, exact: true }).click();
    await expect(reasonItems(page)).toHaveCount(100);
    await expect(checkedRadios(page)).toHaveCount(0);
    lost("leaving the account for the list and coming back", "deleted-accounts page", "all choices");
    await chooseAll(page);
    await page.reload();
    await expect(reasonItems(page)).toHaveCount(100, { timeout: 30_000 });
    await expect(checkedRadios(page)).toHaveCount(0);
    lost("a browser reload", "deleted-accounts page", "all choices; nothing is in storage");
    const storage = await page.evaluate(() => ({ local: Object.keys(localStorage).length, session: Object.keys(sessionStorage).length }));
    expect(storage).toEqual({ local: 0, session: 0 });

    // 4. The save lands and the page read after it is dropped: "Saved." stays true, then Try again reads the rest.
    await chooseAll(page);
    let dropRead = true;
    await page.route(`**${LEGACY}/${fx.id}/page`, async (route) => {
      if (!dropRead) return route.fallback();
      dropRead = false;
      await route.abort("failed");
    });
    await saveButton(page).click();
    await expect(page.getByText(SAVED)).toBeVisible();
    await expect(page.getByText(NOT_SAVED)).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Try again" })).toBeVisible();
    await expect(reasonItems(page)).toHaveCount(0);
    expect(watch.seen.put).toHaveLength(3);
    await checkState(page, "reason-review-session", "legacy-read-dropped");
    await page.getByRole("button", { name: "Try again" }).click();
    await expect(reasonItems(page)).toHaveCount(1);
    await expect(checkedRadios(page)).toHaveCount(0);
    kept("a page read dropped after a save", "deleted-accounts page", "the save itself; the next read starts unchosen");
    await chooseAll(page);
    await saveButton(page).click();
    await expect(page.getByText(ACCOUNT_EMPTY)).toBeVisible();
    watch.stop();
    expect(await pageOf(await apiAs(request, adminName), fx.id)).toEqual([]);
  });

  test("a save, a settle and a read that drop in the delete panel, a changed set, Close and a reload: what the panel keeps at a hundred reasons", async ({ page, request }) => {
    const suffix = `${Date.now().toString(36)}${width}`;
    const fx = await timed("fixture: 110 delete-panel reasons (interruptions)", () => makeMany(request, `${suffix}p`, `Pia Panel ${suffix}`, { askers: 11, groups: 10, withdraw: false, deleted: false }));
    const subject = { id: Number(fx.id), email: fx.subjectEmail };
    await signInTo(page, adminName, "/admin?tab=users");
    const watch = watchDelete(page, subject.id);

    // 1. The walk's second page is dropped: the panel says so and Try again asks for that page only.
    let dropRead = true;
    await page.route(`**/api/v1/admin/users/${subject.id}/erasure-reasons/page`, async (route) => {
      const body = route.request().postData() ? recordOf(JSON.parse(route.request().postData() ?? "{}")) : {};

      if (!dropRead || body.after === null || body.after === undefined) return route.fallback();
      dropRead = false;
      await route.abort("failed");
    });
    await page.getByRole("searchbox", { name: "Search by name or email" }).fill(subject.email);
    const row = deletePanel(page, subject);
    await fromMore(row, "Delete");
    await expect(row.getByText(WALK_FAILED)).toBeVisible({ timeout: 30_000 });
    await expect(row.getByRole("button", { name: "Try again" })).toBeVisible();
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    await expect(row.getByText(DEL_NOT_DELETED)).toHaveCount(0);
    expect(watch.seen.pages).toBe(2);
    await checkState(page, "reason-review-session", "panel-walk-dropped");
    await row.getByRole("button", { name: "Try again" }).click();
    await expect(row.getByText("Reasons 1 to 50 of 110", { exact: true })).toBeVisible({ timeout: 30_000 });
    // The first page was not read again.
    expect(watch.seen.pages).toBe(3);
    kept("a review page read that drops", "delete panel", "the pages already read; Try again continues from the same cursor");

    // 2. Choose by clicking the radios of both display pages, then lose the DELETE and the first settle.
    const chooseDisplayed = async (offset: number) => {
      const n = await panelItems(row).count();
      await row.evaluate((el, picks) => {
        el.querySelectorAll<HTMLElement>("li[data-reason-id]").forEach((li, i) => {
          li.querySelector<HTMLInputElement>(`input[type=radio][value=${picks[i]}]`)?.click();
        });
      }, Array.from({ length: n }, (_, i) => pickOf(offset + i)));
    };

    await chooseDisplayed(0);
    await row.getByRole("button", { name: "Next 50" }).click();
    await expect(row.getByText("Reasons 51 to 100 of 110", { exact: true })).toBeVisible();
    await chooseDisplayed(50);
    await row.getByRole("button", { name: "Next 50" }).click();
    await expect(row.getByText("Reasons 101 to 110 of 110", { exact: true })).toBeVisible();
    await chooseDisplayed(100);
    const blanked = Array.from({ length: 110 }, (_, i) => pickOf(i)).filter((p) => p === "remove").length;
    const summary = `Delete this account. Keep ${110 - blanked} reasons and blank ${blanked}.`;
    await expect(row.getByText(summary)).toBeVisible();
    await expect(row.getByText("All 110 reasons chosen.").first()).toBeVisible();

    let dropDelete = true;
    await page.route(`**/api/v1/admin/users/${subject.id}`, async (route) => {
      if (route.request().method() !== "DELETE" || !dropDelete) return route.fallback();
      dropDelete = false;
      await route.abort("failed");
    });
    await dropFirstSettle(page);
    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByText(DEL_STILL)).toBeVisible();
    await expect(row.getByText(DEL_NOT_DELETED)).toHaveCount(0);
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    await expect(row.getByRole("status").filter({ hasText: "Still checking" })).toBeFocused();
    await checkState(page, "reason-review-session", "panel-unknown-outcome");
    await row.getByRole("button", { name: "Check again" }).click();
    await expect(row.getByText(DEL_NOTHING)).toBeVisible();
    expect(watch.seen.settled).toHaveLength(2);
    expect(watch.seen.settled[0]).toBe(watch.seen.deleted[0]);
    expect(watch.seen.settled[1]).toBe(watch.seen.deleted[0]);
    // Every choice is still there: the panel is the same panel and the store is untouched.
    await expect(row.getByText("All 110 reasons chosen.").first()).toBeVisible();
    await expect(row.getByText(summary)).toBeVisible();
    await expect(row.getByRole("button", { name: "Delete account" })).toBeEnabled();
    kept("a delete that did not run (lost DELETE and settle, then settled not applied)", "delete panel", "all 110 choices; the same panel and store");

    // 3. A reason changes behind the page: the DELETE answers changed, the reload keeps 109 and marks one.
    const changed = fx.reasons[5];
    await (await apiAs(request, changed.login))("POST", `/contact-requests/${changed.requestId}/withdraw`, {});
    await row.getByRole("button", { name: "Delete account" }).click();
    await expect(row.getByRole("alert")).toHaveText(DEL_CHANGED);
    await expect(row.getByText("Choose Keep text or Blank text for 1 more reason.").first()).toBeVisible({ timeout: 60_000 });
    await expect(row.locator('[data-reason-heading="5"]')).toBeFocused();
    await expect(row.getByText(MARKED)).toHaveCount(1);
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    kept("a changed set (409): a requester withdrew one request", "delete panel", "109 of 110 kept; the changed one marked and unchosen");
    lost("a changed set (409): a requester withdrew one request", "delete panel", "the changed reason's choice");
    await checkState(page, "reason-review-session", "panel-changed-at-110");

    // The keyboard path through "Next changed reason" after the 409: move away with Enter, come back
    // to the button with Shift+Tab (it sits before the "Go to reason number" field and its Go button),
    // press it, and the changed reason has focus and is announced before its choice is made.
    await row.getByRole("button", { name: "Next 50" }).focus();
    await page.keyboard.press("Enter");
    await expect(row.getByText("Reasons 51 to 100 of 110", { exact: true })).toBeVisible();
    await expect(row.locator('[data-reason-heading="50"]')).toBeFocused();
    await expect(row.getByText("Showing reasons 51 to 100 of 110.", { exact: true })).toBeAttached();
    await expect(row.locator('[data-reason-heading="5"]')).toHaveCount(0);

    for (let i = 0; i < 3; i++) await page.keyboard.press("Shift+Tab");
    await expect(row.getByRole("button", { name: "Next changed reason" })).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(row.getByText("Reasons 1 to 50 of 110", { exact: true })).toBeVisible();
    await expect(row.locator('[data-reason-heading="5"]')).toBeFocused();
    await expect(row.getByText("Showing reasons 1 to 50 of 110. Reason 6 needs a choice.", { exact: true })).toBeAttached();
    await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
    // Now the one remaining choice, by keys, and the button that found it goes away.
    await page.keyboard.press("Tab");
    await expect(row.getByRole("group", { name: "Reason 6 of 110" }).getByRole("radio", { name: "Keep text" })).toBeFocused();
    await page.keyboard.press(pickOf(5) === "remove" ? "ArrowDown" : "Space");
    await expect(row.getByRole("button", { name: "Next changed reason" })).toHaveCount(0);
    await expect(row.getByText("All 110 reasons chosen.").first()).toBeVisible();
    await expect(row.getByRole("button", { name: "Delete account" })).toBeEnabled();
    kept("Next changed reason by keyboard after a changed set (409)", "delete panel", "focus and the announcement reach the changed reason; the choice completes the review by keys");

    // 4. Close and open again: the review is read afresh and nothing is chosen.
    const mintedBefore = watch.seen.minted;
    await row.getByRole("button", { name: "Cancel" }).click();
    const again = await openDeletePanel(page, subject, 110);
    await expect(again.getByText("Choose Keep text or Blank text for 110 more reasons.").first()).toBeVisible();
    await expect(again.getByRole("radio", { checked: true })).toHaveCount(0);
    await expect(again.getByRole("button", { name: "Delete account" })).toBeDisabled();
    expect(watch.seen.minted).toBe(mintedBefore);
    lost("Cancel, then Delete again on the same person", "delete panel", "all choices; the reasons are read again");

    // 5. A browser reload loses them too, and nothing is stored.
    await again.locator('[data-reason-heading="0"]').focus();
    await page.keyboard.press("Tab");
    await page.keyboard.press("Space");
    await expect(again.getByText("Choose Keep text or Blank text for 109 more reasons.").first()).toBeVisible();
    await page.reload();
    await expect(page.getByRole("searchbox", { name: "Search by name or email" })).toBeVisible();
    await page.getByRole("searchbox", { name: "Search by name or email" }).fill(subject.email);
    const reloaded = deletePanel(page, subject);
    await fromMore(reloaded, "Delete");
    await expect(reloaded.getByText("Choose Keep text or Blank text for 110 more reasons.").first()).toBeVisible({ timeout: 60_000 });
    lost("a browser reload", "delete panel", "all choices; nothing is in storage");
    const storage = await page.evaluate(() => ({ local: Object.keys(localStorage).length, session: Object.keys(sessionStorage).length }));
    expect(storage).toEqual({ local: 0, session: 0 });
    watch.stop();

    // The person is still there, with every reason as it was but the one withdrawn.
    const admin = await apiAs(request, adminName);
    expect(stringAt(await admin("GET", `/contact-requests/${fx.reasons[0].requestId}`), "request", "reason")).toBe(fx.reasons[0].text);
    expect(stringAt(await admin("GET", `/contact-requests/${changed.requestId}`), "request", "reason")).toBe(changed.text);
  });

  test("on the deleted-accounts page an attempt that ran out, one the server forgot and a session that ended keep, lose and say different things", async ({ page, request }) => {
    const suffix = `${Date.now().toString(36)}${width}`;
    const fx = await makeLegacy(request, `${suffix}x`, `Xena Expired ${suffix}`, 3);
    await setClock(request, 0);

    try {
      await signInTo(page, adminName, accountPath(fx.id));
      await expect(reasonItems(page)).toHaveCount(3);
      await chooseAll(page);
      const watch = watchSave(page, fx.id);

      // An id older than ten minutes is not run. The page says Fold did not run it, settles that
      // same id, and the page read after it keeps every choice, because the reasons are unchanged.
      await ageFirstPut(page, fx.id, request, 11 * 60);
      const release = await holdSettle(page);
      await saveButton(page).click();
      await expect(page.getByRole("status").filter({ hasText: "too old" })).toBeVisible();
      await expect(page.getByText(NOT_SAVED)).toHaveCount(0);
      release();
      await expect(page.getByText(NOT_APPLIED)).toBeVisible();
      await expect(checkedRadios(page)).toHaveCount(3);
      kept("an attempt older than 10 minutes (clock shifted 11 minutes)", "deleted-accounts page", "all choices; settled not applied, page read again");
      await setClock(request, 0);
      await page.unroute(`**${SETTLE}`);
      await page.unroute(`**${LEGACY}/${fx.id}`);

      // An id the server no longer remembers (over an hour, or a restart) is "can't tell": Save stays
      // off, and a Reload that works starts the page afresh with no choice kept.
      await ageFirstPut(page, fx.id, request, 61 * 60 + 30);
      await saveButton(page).click();
      await expect(page.getByText(HISTORY)).toBeVisible();
      await expect(page.getByText(NOT_SAVED)).toHaveCount(0);
      await expect(saveButton(page)).toBeDisabled();
      await expect.poll(() => focusedText(page)).toContain("Fold can't tell");
      await checkState(page, "reason-review-session", "legacy-history-unavailable");
      await setClock(request, 0);
      await page.getByRole("button", { name: "Reload" }).click();
      await expect(page.getByText(HISTORY)).toHaveCount(0);
      await expect(reasonItems(page)).toHaveCount(3);
      await expect(checkedRadios(page)).toHaveCount(0);
      lost("an attempt the server forgot (clock shifted 61 minutes, standing in for a restart)", "deleted-accounts page", "all choices; the Reload starts clean");
      await page.unroute(`**${LEGACY}/${fx.id}`);

      // A session that ends mid-review: another admin sets this admin's password, which ends every
      // session they have. The next press is refused and nothing is saved.
      const second = await makePerson(await apiAs(request, adminName), `mn-second-${suffix}x`, `Sandy Second ${suffix}`, "admin");
      await signInTo(page, `mn-second-${suffix}x`, accountPath(fx.id));
      await expect(reasonItems(page)).toHaveCount(3);
      await chooseAll(page);
      await endSessionsOf(request, second.id);
      await saveButton(page).click();
      await expect(page.getByText(SAVED)).toHaveCount(0);
      await expect(page.getByRole("heading", { name: /^Log in to/ }).or(page.getByText(SIGNED_OUT).first()).first()).toBeVisible({ timeout: 20_000 });
      await checkState(page, "reason-review-session", "legacy-session-ended");
      watch.stop();
      lost("the session ending before Save (the admin's sessions ended server-side)", "deleted-accounts page", "all choices; the page asks for sign-in");
      expect(await pageOf(await apiAs(request, adminName), fx.id)).toHaveLength(3);
      await signInTo(page, `mn-second-${suffix}x`, accountPath(fx.id));
      await expect(reasonItems(page)).toHaveCount(3);
      await expect(checkedRadios(page)).toHaveCount(0);
    } finally {
      await setClock(request, 0);
    }
  });

  test("in the delete panel an attempt that ran out, one the server forgot, a restore and a session that ended keep, lose and say different things", async ({ page, request }) => {
    const suffix = `${Date.now().toString(36)}${width}`;
    const fx = await makeMany(request, `${suffix}z`, `Zed Panel ${suffix}`, { askers: 2, groups: 2, withdraw: false, deleted: false });
    const subject = { id: Number(fx.id), email: fx.subjectEmail };
    await setClock(request, 0);

    const chooseAllDisplayed = async (row: Locator) => {
      const n = await panelItems(row).count();
      await row.evaluate((el, picks) => {
        el.querySelectorAll<HTMLElement>("li[data-reason-id]").forEach((li, i) => li.querySelector<HTMLInputElement>(`input[type=radio][value=${picks[i]}]`)?.click());
      }, Array.from({ length: n }, (_, i) => pickOf(i)));
      await expect(row.getByText("All 4 reasons chosen.").first()).toBeVisible();
    };

    try {
      await signInTo(page, adminName, "/admin?tab=users");
      const watch = watchDelete(page, subject.id);
      let row = await openDeletePanel(page, subject, 4);
      await chooseAllDisplayed(row);

      // Older than ten minutes: not run, settled to nothing deleted, every choice still there.
      await ageFirstDelete(page, subject.id, request, 11 * 60);
      const release = await holdSettle(page);
      await row.getByRole("button", { name: "Delete account" }).click();
      await expect(row.getByRole("status").filter({ hasText: "too old to run" })).toBeVisible();
      await expect(row.getByText(DEL_NOT_DELETED)).toHaveCount(0);
      release();
      await expect(row.getByText(DEL_NOTHING)).toBeVisible();
      await expect(row.getByText("All 4 reasons chosen.").first()).toBeVisible();
      await expect(row.getByRole("button", { name: "Delete account" })).toBeEnabled();
      kept("an attempt older than 10 minutes (clock shifted 11 minutes)", "delete panel", "all choices; the same panel and store");
      await setClock(request, 0);
      await page.unroute(`**${SETTLE}`);
      await page.unroute(`**/api/v1/admin/users/${subject.id}`);

      // Not remembered any more (over an hour, or a restart): "can't tell", Delete off. Close reloads
      // the people list; opening Delete again reads the reasons afresh with nothing chosen.
      await ageFirstDelete(page, subject.id, request, 61 * 60 + 30);
      await row.getByRole("button", { name: "Delete account" }).click();
      await expect(row.getByText(DEL_HISTORY)).toBeVisible();
      await expect(row.getByText(DEL_NOT_DELETED)).toHaveCount(0);
      await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
      await expect(row.getByRole("status").filter({ hasText: "Fold can't tell" })).toBeFocused();
      await checkState(page, "reason-review-session", "panel-history-unavailable");
      await setClock(request, 0);
      await row.getByRole("button", { name: "Close" }).click();
      await expect(deletePanel(page, subject)).toHaveCount(1);
      row = await openDeletePanel(page, subject, 4);
      await expect(row.getByText(DEL_HISTORY)).toHaveCount(0);
      await expect(row.getByText("Choose Keep text or Blank text for 4 more reasons.").first()).toBeVisible();
      lost("an attempt the server forgot (clock shifted 61 minutes, standing in for a restart)", "delete panel", "all choices; Close and a fresh read start clean");
      await page.unroute(`**/api/v1/admin/users/${subject.id}`);

      // A restore the server noticed (the answer is made up here, not a real restore): Delete is off
      // until the list loads afresh, and the review that follows starts clean.
      await chooseAllDisplayed(row);
      await page.route(`**/api/v1/admin/users/${subject.id}`, (route) =>
        route.request().method() === "DELETE" ? route.fulfill({ status: 503, json: { error: "restore", code: "restore_detected" } }) : route.fallback());
      await row.getByRole("button", { name: "Delete account" }).click();
      await expect(row.getByRole("alert")).toHaveText(DEL_RESTORE);
      await expect(row.getByRole("button", { name: "Delete account" })).toBeDisabled();
      await checkState(page, "reason-review-session", "panel-restore-detected");
      await page.unroute(`**/api/v1/admin/users/${subject.id}`);
      await row.getByRole("button", { name: "Close" }).click();
      await expect(deletePanel(page, subject)).toHaveCount(1);
      row = await openDeletePanel(page, subject, 4);
      await expect(row.getByText("Choose Keep text or Blank text for 4 more reasons.").first()).toBeVisible();
      lost("restore_detected (a made-up answer, not a real restore)", "delete panel", "all choices; Close and a fresh read start clean");

      // A session that ends mid-review (another admin set this admin's password): the press is
      // refused and nothing is deleted.
      const second = await makePerson(await apiAs(request, adminName), `mn-second-${suffix}z`, `Sandy Second ${suffix}`, "admin");
      await signInTo(page, `mn-second-${suffix}z`, "/admin?tab=users");
      row = await openDeletePanel(page, subject, 4);
      await chooseAllDisplayed(row);
      await endSessionsOf(request, second.id);
      await row.getByRole("button", { name: "Delete account" }).click();
      await expect(page.getByText(DELETED)).toHaveCount(0);
      await expect(page.getByRole("heading", { name: /^Log in to/ }).or(page.getByText(SIGNED_OUT).first()).first()).toBeVisible({ timeout: 20_000 });
      await checkState(page, "reason-review-session", "panel-session-ended");
      watch.stop();
      lost("the session ending before Delete (the admin's sessions ended server-side)", "delete panel", "all choices; the page asks for sign-in");
      const again = await apiAs(request, adminName);
      expect(stringAt(await again("GET", `/contact-requests/${fx.reasons[0].requestId}`), "request", "reason")).toBe(fx.reasons[0].text);
    } finally {
      await setClock(request, 0);
    }
  });
});

// Setting this person's password, as another admin can, ends every session they have (replacePasswordIn).
async function endSessionsOf(request: APIRequestContext, userId: number) {
  const admin = await apiAs(request, adminName);
  await admin("POST", `/admin/users/${userId}/password`, { password: PASSWORD });
}

const reasonItem = (page: Page, text: string) => reasonList(page).getByRole("listitem").filter({ hasText: text });

// The clock moves the moment the first PUT (or DELETE) leaves, so the id minted a moment earlier is
// that old when it arrives.
async function ageFirstPut(page: Page, id: string, request: APIRequestContext, seconds: number) {
  let first = true;
  await page.route(`**${LEGACY}/${id}`, async (route) => {
    if (route.request().method() !== "PUT" || !first) return route.fallback();
    first = false;
    await setClock(request, seconds);
    await route.fallback();
  });
}

async function ageFirstDelete(page: Page, id: number, request: APIRequestContext, seconds: number) {
  let first = true;
  await page.route(`**/api/v1/admin/users/${id}`, async (route) => {
    if (route.request().method() !== "DELETE" || !first) return route.fallback();
    first = false;
    await setClock(request, seconds);
    await route.fallback();
  });
}

// Holds the settle call until released, so the checking state can be read.
async function holdSettle(page: Page) {
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  await page.route(`**${SETTLE}`, async (route) => {
    await gate;
    await route.fallback();
  });

  return release;
}
