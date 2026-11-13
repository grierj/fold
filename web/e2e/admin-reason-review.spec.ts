import AxeBuilder from "@axe-core/playwright";
import { mkdirSync } from "node:fs";
import { expect, test, type APIRequestContext, type Page, type Request, type Route } from "@playwright/test";
import { apiAs, nextFrames, signInTo } from "./helpers";
import { makeLegacy, makePerson, numberAt, pageOf, rawAdmin, recordOf, stringAt, type Admin } from "./legacy-reasons";
// @ts-expect-error plain JS module shared with verify-ui
import { measure } from "./ui-measure.mjs";

// The deleted-accounts review (M7.4 T07B-3b, T-302): the page an admin uses to
// decide the contact-request reasons that a deletion left undecided. Every
// scenario makes its own people, so this spec depends on no other spec and no
// seed user besides the admin.
//
// No live path leaves a deleted account with undecided reasons (a deletion
// decides every reason it reaches), so each scenario builds the fixture as the
// people would (a leader asks a member for contact details, with a reason) and
// then soft-deletes the member through the test binary's one-row seed route
// (attempt_clock_e2e.go, built only with -tags e2eclock). That route refuses the
// caller and any admin; a deleted admin is covered by the Go tests.

const adminName = "grier";
const proof = process.env.SCREENS_DIR;
const WIDTHS = [390, 1280] as const;

const MINT = "/api/v1/admin/operation-attempts";
const SETTLE = "/api/v1/admin/operation-attempts/settle";
const LEGACY = "/api/v1/admin/erasure-reasons/legacy";

const TITLE = "Deleted accounts with reasons to review";
const INTRO = /These deleted accounts still have contact-request reasons nobody has reviewed/;
const LIMIT = /Fold lists only requests this person made or was asked about/;
const ADMINS_ONLY = "This page is for church admins only.";
const NO_ACCOUNTS = "No deleted accounts have reasons left to review.";
const ACCOUNT_EMPTY = "This account has no reasons left to review.";
const PRE_SAVE = "Saving blanks the text you chose right away. It can't be brought back in Fold. Backups made before now still contain it.";
const SAVED = "Saved.";
const CHANGED = "Some reasons changed since you loaded them. Check the marked ones, then save again.";
const CHECKING = "Checking what happened to this save…";
const NOT_APPLIED = "These choices weren't saved. Reload to see what's left.";
const TOO_OLD = "This save was too old to run, so Fold didn't run it. Fold is checking what happened.";
const NOT_SAVED = /weren't saved|not saved|wasn't saved|was not saved/i;

// Decides some of an account's reasons through the API, as another admin session
// would: one mint, one PUT.
async function decideThroughApi(request: APIRequestContext, id: string, only: string, choice: "keep" | "remove") {
  const admin = await apiAs(request, adminName);
  const items = await pageOf(admin, id);
  const item = items.find((it) => it.reason.includes(only));
  if (!item) throw new Error(`no reason holding ${only}`);
  const minted = await admin("POST", "/admin/operation-attempts", { kind: "legacy_reason_save", subjectUserId: Number(id) });
  await admin("PUT", `/admin/erasure-reasons/legacy/${id}`, {
    attemptId: stringAt(minted, "attemptId"),
    decisions: [{ requestId: item.requestId, reviewHash: item.reviewHash, choice }],
  });
}

// The reason a contact request holds now, as the admin reads it.
async function reasonOf(request: APIRequestContext, requestId: number): Promise<string> {
  const admin = await apiAs(request, adminName);
  return stringAt(await admin("GET", `/contact-requests/${requestId}`), "request", "reason");
}

const reasonList = (page: Page) => page.getByRole("list", { name: "Contact-request reasons" });
const reasonItem = (page: Page, text: string) => reasonList(page).getByRole("listitem").filter({ hasText: text });
const accountLink = (page: Page, id: string) => page.getByRole("list", { name: "Deleted accounts" }).getByRole("link", { name: `Deleted account · ref ${id}`, exact: true });
const refHeading = (page: Page, id: string) => page.getByRole("heading", { name: `Deleted account · ref ${id}`, level: 1, exact: true });
const save = (page: Page) => page.getByRole("button", { name: "Save", exact: true });

async function choose(page: Page, text: string, choice: "Keep text" | "Blank text") {
  await reasonItem(page, text).getByRole("radio", { name: choice }).check();
}

// The requests the page sends for one save: how many ids it minted, what each
// PUT and settle carried. An id is a capability, so it is compared here and
// never printed.
function watchSave(page: Page, id: string) {
  const seen = { minted: 0, put: [] as string[], settled: [] as string[], pages: 0 };
  const onRequest = (r: Request) => {
    const path = new URL(r.url()).pathname;
    const body = r.postData() ? recordOf(JSON.parse(r.postData() ?? "{}")) : {};
    if (r.method() === "POST" && path === MINT && body.kind === "legacy_reason_save") seen.minted++;
    if (r.method() === "PUT" && path === `${LEGACY}/${id}` && typeof body.attemptId === "string") seen.put.push(body.attemptId);
    if (r.method() === "POST" && path === SETTLE && typeof body.attemptId === "string") seen.settled.push(body.attemptId);
    if (r.method() === "POST" && path === `${LEGACY}/${id}/page`) seen.pages++;
  };
  page.on("request", onRequest);
  return { seen, stop: () => page.off("request", onRequest) };
}

// The PUT for this account is answered by `how` the first time it is sent: with
// run, the real PUT happens first (so the save lands) and its answer is dropped,
// which is what a lost connection looks like; without, it never leaves.
async function loseFirstPut(page: Page, id: string, run: boolean) {
  let first = true;
  await page.route(`**${LEGACY}/${id}`, async (route: Route) => {
    if (route.request().method() !== "PUT" || !first) return route.fallback();
    first = false;
    if (run) await route.fetch();
    await route.abort("failed");
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

async function setClock(admin: Admin, offsetSeconds: number) {
  await admin("POST", "/e2e/attempt-clock", { offsetSeconds });
}

// Moves the attempt clock the moment the first PUT leaves, so the id minted a
// moment earlier is that old when it arrives.
async function ageFirstPut(page: Page, id: string, admin: Admin, seconds: number) {
  let first = true;
  await page.route(`**${LEGACY}/${id}`, async (route) => {
    if (route.request().method() !== "PUT" || !first) return route.fallback();
    first = false;
    await setClock(admin, seconds);
    await route.fallback();
  });
}

// One state at 390 and 1280: axe and layout checks first, then the screenshot
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
      await page.screenshot({ path: `${proof}/admin-reason-review-${state}-${width}.png`, fullPage: true, animations: "disabled" });
    }
  }
  await page.setViewportSize({ width: 1280, height: 900 });
}

// The element holding focus, as the words it shows: the message box after an answer.
async function focusedText(page: Page): Promise<string> {
  return page.evaluate(() => document.activeElement?.textContent ?? "");
}

test.describe("the deleted-accounts review", () => {
  test.use({ locale: "en-US", timezoneId: "America/Boise" });
  test.setTimeout(120_000);

  test("two people with one former name are told apart by ref, never named, and a mixed save is kept as chosen", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const former = `Dana Duplicate ${suffix}`;
    const a = await makeLegacy(request, `${suffix}a`, former);
    const b = await makeLegacy(request, `${suffix}b`, former);
    expect(a.id).not.toBe(b.id);

    // The seed route says nothing happened for an account that is already gone.
    const raw = await rawAdmin(request);
    expect((await raw("POST", "/e2e/legacy-tombstone", { userId: Number(a.id) })).status()).toBe(404);

    await signInTo(page, adminName, "/admin?tab=users");
    // The link carries the badge in its own accessible name.
    const link = page.getByRole("link", { name: /Deleted accounts with reasons to review/ });
    await expect(link).toContainText("Needs review");
    await expect(link).toHaveAccessibleName(`${TITLE} Needs review`);
    await link.click();
    await expect(page).toHaveURL(/\/admin\/deleted-reasons$/);
    await expect(page.getByRole("heading", { name: TITLE, level: 1 })).toBeVisible();
    await expect(page.getByText(INTRO)).toBeVisible();
    await expect(page.getByText(LIMIT)).toBeVisible();
    await expect(accountLink(page, a.id)).toBeVisible();
    await expect(accountLink(page, b.id)).toBeVisible();
    await expect(page.getByText("Checking…", { exact: true })).toHaveCount(0);
    await expect(page.locator("body")).not.toContainText(former);
    await expect(page.locator("body")).not.toContainText(a.subjectEmail);
    await evidence(page, "list");

    await accountLink(page, a.id).click();
    await expect(refHeading(page, a.id)).toBeVisible();
    for (const text of a.texts) await expect(reasonItem(page, text)).toBeVisible();
    await expect(page.locator("body")).not.toContainText(former);
    // Nothing is chosen for the admin and nothing is bulk.
    await expect(page.getByRole("radio", { checked: true })).toHaveCount(0);
    await expect(page.getByRole("radio", { name: "Keep text" })).toHaveCount(2);
    await expect(page.getByRole("button", { name: /all/i })).toHaveCount(0);
    await expect(save(page)).toBeDisabled();
    await expect(page.getByText("Choose Keep text or Blank text for 2 more reasons.")).toBeVisible();
    await expect(page.getByText(PRE_SAVE)).toHaveCount(0);
    await evidence(page, "account-unchosen");

    await choose(page, a.texts[0], "Keep text");
    await expect(save(page)).toBeDisabled();
    await choose(page, a.texts[1], "Blank text");
    await expect(save(page)).toBeEnabled();
    await expect(page.getByText(PRE_SAVE)).toBeVisible();
    await evidence(page, "account-chosen");

    const watch = watchSave(page, a.id);
    await save(page).click();
    await expect(page.getByRole("status").filter({ hasText: SAVED })).toBeVisible();
    await expect(page.getByText(ACCOUNT_EMPTY)).toBeVisible();
    await expect(save(page)).toHaveCount(0);
    await expect.poll(() => focusedText(page)).toContain(SAVED);
    await evidence(page, "saved-empty");
    watch.stop();
    expect(watch.seen.minted).toBe(1);
    expect(watch.seen.put).toHaveLength(1);
    expect(watch.seen.settled).toEqual([]);

    // The server holds exactly what was chosen: the first reason kept, the second blank, and none left to review.
    const admin = await apiAs(request, adminName);
    expect(await pageOf(admin, a.id)).toEqual([]);
    expect(await reasonOf(request, a.requestIds[0])).toBe(a.texts[0]);
    expect(await reasonOf(request, a.requestIds[1])).toBe("");

    // The account that was saved is gone from the list and the other stays.
    await page.getByRole("link", { name: TITLE }).click();
    await expect(page.getByRole("heading", { name: TITLE, level: 1 })).toBeVisible();
    await expect(accountLink(page, b.id)).toBeVisible();
    await expect(accountLink(page, a.id)).toHaveCount(0);

    await accountLink(page, b.id).click();
    await expect(refHeading(page, b.id)).toBeVisible();
    await expect(reasonItem(page, b.texts[0])).toBeVisible();
    await expect(page.locator("body")).not.toContainText(former);
  });

  test("the seed route refuses the caller, an admin, a missing account and a non-admin", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const admin = await apiAs(request, adminName);
    const other = await makePerson(admin, `lg-other-admin-${suffix}`, `Ola Other ${suffix}`, "admin");
    const me = await (await rawAdmin(request))("GET", "/auth/me");
    const mine = numberAt(await me.json(), "user", "id");
    const asAdmin = await rawAdmin(request);
    expect((await asAdmin("POST", "/e2e/legacy-tombstone", { userId: mine })).status()).toBe(400);
    expect((await asAdmin("POST", "/e2e/legacy-tombstone", { userId: other.id })).status()).toBe(400);
    expect((await asAdmin("POST", "/e2e/legacy-tombstone", { userId: 2147483000 })).status()).toBe(404);
    expect((await asAdmin("POST", "/e2e/legacy-tombstone", {})).status()).toBe(400);
    // Neither refusal deleted anyone: the other admin still signs in.
    await signInTo(page, `lg-other-admin-${suffix}`, "/admin?tab=users");
    await expect(page.getByRole("button", { name: "New user" })).toBeVisible();

    // A member cannot reach the route, and cannot use the page.
    await apiAs(request, "sam");
    const csrf = stringAt(await (await request.get("/api/v1/auth/csrf")).json(), "csrfToken");
    const sam = await request.fetch("/api/v1/e2e/legacy-tombstone", { method: "POST", data: { userId: other.id }, headers: { "X-CSRF-Token": csrf } });
    expect(sam.status()).toBe(403);
    const reads: string[] = [];
    page.on("request", (r) => { if (new URL(r.url()).pathname.startsWith(LEGACY)) reads.push(r.url()); });
    await signInTo(page, "sam", "/admin/deleted-reasons");
    await expect(page.getByRole("heading", { name: TITLE, level: 1 })).toBeVisible();
    await expect(page.getByText(ADMINS_ONLY)).toBeVisible();
    expect(reads).toEqual([]);
    await evidence(page, "not-admin");
  });

  test("a reason decided elsewhere shows the changed copy, drops that reason, keeps the other choice and the next save mints again", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const fx = await makeLegacy(request, `${suffix}c`, `Cleo Changed ${suffix}`, 3);
    await signInTo(page, adminName, `/admin/deleted-reasons?account=${fx.id}`);
    await expect(refHeading(page, fx.id)).toBeVisible();
    for (const text of fx.texts) await expect(reasonItem(page, text)).toBeVisible();
    await choose(page, fx.texts[0], "Keep text");
    await choose(page, fx.texts[1], "Blank text");
    await choose(page, fx.texts[2], "Keep text");
    await expect(save(page)).toBeEnabled();

    // Another admin session decides the middle reason first.
    await decideThroughApi(request, fx.id, fx.texts[1], "keep");

    const watch = watchSave(page, fx.id);
    await save(page).click();
    await expect(page.getByRole("alert")).toContainText(CHANGED);
    await expect(page.getByRole("alert")).not.toContainText("delete");
    // The decided reason is gone, the others keep what was chosen, nothing is marked.
    await expect(reasonItem(page, fx.texts[1])).toHaveCount(0);
    await expect(reasonItem(page, fx.texts[0]).getByRole("radio", { name: "Keep text" })).toBeChecked();
    await expect(reasonItem(page, fx.texts[2]).getByRole("radio", { name: "Keep text" })).toBeChecked();
    await expect(save(page)).toBeEnabled();
    await evidence(page, "changed");

    await save(page).click();
    await expect(page.getByRole("status").filter({ hasText: SAVED })).toBeVisible();
    await expect(page.getByText(ACCOUNT_EMPTY)).toBeVisible();
    watch.stop();
    expect(watch.seen.minted).toBe(2);
    expect(watch.seen.put).toHaveLength(2);
    expect(watch.seen.put[0]).not.toBe(watch.seen.put[1]);
    expect(watch.seen.settled).toEqual([]);
  });

  test("a lost answer to a save that never ran says nothing before settle, then says it was not saved, and a fresh press saves", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const fx = await makeLegacy(request, `${suffix}u`, `Uma Unsure ${suffix}`, 2);
    await signInTo(page, adminName, `/admin/deleted-reasons?account=${fx.id}`);
    await expect(reasonItem(page, fx.texts[0])).toBeVisible();
    await choose(page, fx.texts[0], "Keep text");
    await choose(page, fx.texts[1], "Blank text");
    const watch = watchSave(page, fx.id);
    await loseFirstPut(page, fx.id, false);
    const release = await holdSettle(page);

    await save(page).click();
    const status = page.getByRole("status").filter({ hasText: "Checking what happened" });
    await expect(status).toHaveText(CHECKING);
    await expect(page.getByText(NOT_SAVED)).toHaveCount(0);
    await expect(save(page)).toBeDisabled();
    await expect(page.getByRole("radio", { name: "Keep text" }).first()).toBeDisabled();
    await evidence(page, "checking");

    release();
    await expect(page.getByText(NOT_APPLIED)).toBeVisible();
    // The reload keeps both choices, because nothing about the reasons changed.
    await expect(reasonItem(page, fx.texts[0]).getByRole("radio", { name: "Keep text" })).toBeChecked();
    await expect(reasonItem(page, fx.texts[1]).getByRole("radio", { name: "Blank text" })).toBeChecked();
    await expect(save(page)).toBeEnabled();
    await evidence(page, "not-applied");

    await save(page).click();
    await expect(page.getByRole("status").filter({ hasText: SAVED })).toBeVisible();
    await expect(page.getByText(ACCOUNT_EMPTY)).toBeVisible();
    watch.stop();
    expect(watch.seen.minted).toBe(2);
    expect(watch.seen.put).toHaveLength(2);
    expect(watch.seen.put[0]).not.toBe(watch.seen.put[1]);
    // Only the lost id is settled, and it is the one the first PUT carried.
    expect(watch.seen.settled).toEqual([watch.seen.put[0]]);
  });

  test("a lost answer to a save that ran settles to saved with no second PUT", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const fx = await makeLegacy(request, `${suffix}r`, `Ria Ran ${suffix}`, 2);
    await signInTo(page, adminName, `/admin/deleted-reasons?account=${fx.id}`);
    await expect(reasonItem(page, fx.texts[0])).toBeVisible();
    await choose(page, fx.texts[0], "Blank text");
    await choose(page, fx.texts[1], "Blank text");
    const watch = watchSave(page, fx.id);
    await loseFirstPut(page, fx.id, true);

    await save(page).click();
    await expect(page.getByRole("status").filter({ hasText: SAVED })).toBeVisible();
    await expect(page.getByText(ACCOUNT_EMPTY)).toBeVisible();
    await expect(page.getByText(NOT_SAVED)).toHaveCount(0);
    watch.stop();
    expect(watch.seen.minted).toBe(1);
    expect(watch.seen.put).toHaveLength(1);
    expect(watch.seen.settled).toEqual(watch.seen.put);
    expect(await pageOf(await apiAs(request, adminName), fx.id)).toEqual([]);
  });

  test("an id older than ten minutes says Fold did not run it, settles that same id to not saved, and a fresh press saves", async ({ page, request }) => {
    test.skip(process.env.E2E_NO_CLOCK === "1", "E2E_NO_CLOCK=1: this binary has no attempt clock");
    const suffix = Date.now().toString(36);
    const fx = await makeLegacy(request, `${suffix}o`, `Olga Old ${suffix}`, 1);
    const admin = await apiAs(request, adminName);
    await setClock(admin, 0);
    try {
      await signInTo(page, adminName, `/admin/deleted-reasons?account=${fx.id}`);
      await expect(reasonItem(page, fx.texts[0])).toBeVisible();
      await choose(page, fx.texts[0], "Keep text");
      const watch = watchSave(page, fx.id);
      await ageFirstPut(page, fx.id, admin, 11 * 60);
      const release = await holdSettle(page);

      await save(page).click();
      await expect(page.getByRole("status").filter({ hasText: "too old" })).toHaveText(TOO_OLD);
      await expect(page.getByText(NOT_SAVED)).toHaveCount(0);
      await expect(save(page)).toBeDisabled();
      await evidence(page, "expired");

      release();
      await expect(page.getByText(NOT_APPLIED)).toBeVisible();
      await expect(save(page)).toBeEnabled();
      await save(page).click();
      await expect(page.getByRole("status").filter({ hasText: SAVED })).toBeVisible();
      watch.stop();
      expect(watch.seen.minted).toBe(2);
      expect(watch.seen.put).toHaveLength(2);
      expect(watch.seen.put[0]).not.toBe(watch.seen.put[1]);
      expect(watch.seen.settled).toEqual([watch.seen.put[0]]);
    } finally {
      await setClock(admin, 0);
    }
  });

  test("leaving mid-settle drops the attempt, and the account is gone from the list once it is opened again", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const fx = await makeLegacy(request, `${suffix}l`, `Lena Left ${suffix}`, 1);
    await signInTo(page, adminName, "/admin/deleted-reasons");
    await expect(accountLink(page, fx.id)).toBeVisible();
    await accountLink(page, fx.id).click();
    await expect(reasonItem(page, fx.texts[0])).toBeVisible();
    await choose(page, fx.texts[0], "Blank text");
    const watch = watchSave(page, fx.id);
    await loseFirstPut(page, fx.id, true);
    const release = await holdSettle(page);

    try {
      await save(page).click();
      await expect(page.getByRole("status").filter({ hasText: "Checking what happened" })).toBeVisible();
      await page.getByRole("link", { name: TITLE }).click();
      await expect(page.getByRole("heading", { name: TITLE, level: 1 })).toBeVisible();
      const response = page.waitForResponse((r) =>
        new URL(r.url()).pathname === SETTLE && r.request().method() === "POST"
        && recordOf(JSON.parse(r.request().postData() ?? "{}")).attemptId === watch.seen.put[0],
      );
      release();
      const settled = await response;
      expect(await settled.finished()).toBeNull();
      expect(await settled.json()).toEqual({ result: "applied" });
      // Let the browser process the completed response and its React updates.
      await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
    } finally {
      release();
    }
    expect(watch.seen.settled).toEqual([watch.seen.put[0]]);
    await expect(page.getByText(SAVED)).toHaveCount(0);

    // Opening it again reads a fresh audited page, which is empty, and the entry leaves the list.
    await accountLink(page, fx.id).click();
    await expect(page.getByText(ACCOUNT_EMPTY)).toBeVisible();
    await page.getByRole("link", { name: TITLE }).click();
    await expect(page.getByRole("heading", { name: TITLE, level: 1 })).toBeVisible();
    await expect(accountLink(page, fx.id)).toHaveCount(0);
    watch.stop();
    expect(watch.seen.minted).toBe(1);
    expect(watch.seen.put).toHaveLength(1);
  });

  test("the keyboard reaches every choice and Save, and focus lands on the answer", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const fx = await makeLegacy(request, `${suffix}k`, `Kay Keys ${suffix}`, 2);
    await signInTo(page, adminName, `/admin/deleted-reasons?account=${fx.id}`);
    await expect(reasonItem(page, fx.texts[0])).toBeVisible();

    // From the link back to the list, Tab enters the first group on its first radio.
    await page.getByRole("link", { name: TITLE }).focus();
    await page.keyboard.press("Tab");
    await expect(reasonItem(page, fx.texts[0]).getByRole("radio", { name: "Keep text" })).toBeFocused();
    await page.keyboard.press("Space");
    await expect(reasonItem(page, fx.texts[0]).getByRole("radio", { name: "Keep text" })).toBeChecked();
    await page.keyboard.press("Tab");
    await expect(reasonItem(page, fx.texts[1]).getByRole("radio", { name: "Keep text" })).toBeFocused();
    await page.keyboard.press("ArrowDown");
    await expect(reasonItem(page, fx.texts[1]).getByRole("radio", { name: "Blank text" })).toBeChecked();
    await expect(reasonItem(page, fx.texts[1]).getByRole("radio", { name: "Blank text" })).toBeFocused();
    await page.keyboard.press("Tab");
    await expect(save(page)).toBeFocused();
    await evidence(page, "keyboard-focus");
    await page.keyboard.press("Enter");
    await expect(page.getByRole("status").filter({ hasText: SAVED })).toBeVisible();
    await expect.poll(() => focusedText(page)).toContain(SAVED);
    // Space and Enter reach nothing disabled: Save is gone with the page.
    await expect(save(page)).toHaveCount(0);
  });

  test("an empty unit that leads on is followed, on the list and on an account", async ({ page, request }) => {
    const suffix = Date.now().toString(36);
    const fx = await makeLegacy(request, `${suffix}e`, `Eve Empty ${suffix}`, 1);
    const seen: string[] = [];
    // The first answer on each is an empty unit that leads on from the start of the table.
    let listFirst = true;
    await page.route(`**${LEGACY}?**`, async (route) => {
      const url = new URL(route.request().url());
      seen.push(`${url.pathname}${url.search}`);
      return route.fallback();
    });
    await page.route(`**${LEGACY}`, async (route) => {
      if (route.request().method() !== "GET" || !listFirst) return route.fallback();
      listFirst = false;
      seen.push("list-first");
      await route.fulfill({ json: { accounts: [], next: "0" } });
    });
    await signInTo(page, adminName, "/admin/deleted-reasons");
    await expect(accountLink(page, fx.id)).toBeVisible();
    await expect(page.getByText(NO_ACCOUNTS)).toHaveCount(0);
    expect(seen).toContain("list-first");
    expect(seen.some((s) => s.endsWith("?after=0"))).toBe(true);

    let pageFirst = true;
    await page.route(`**${LEGACY}/${fx.id}/page`, async (route) => {
      if (!pageFirst) return route.fallback();
      pageFirst = false;
      await route.fulfill({ json: { items: [], next: "0" } });
    });
    await accountLink(page, fx.id).click();
    await expect(reasonItem(page, fx.texts[0])).toBeVisible();
    await expect(page.getByText(ACCOUNT_EMPTY)).toHaveCount(0);
    expect(pageFirst).toBe(false);
  });
});
