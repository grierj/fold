import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Link, MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AdminUsers } from "../api/adminUsers";
import { ApiError } from "../api/core";
import { needsRestoreReload, reloadStarted, reloadSucceeded } from "../lib/deleteReload";
import { ERROR_COPY } from "../lib/errors";
import type { ErasureReasonItem, ErasureReasonPage, LegacyDiscoveryAccount, LegacyReasonDiscovery } from "../types";
import { ReasonReviewLegacyPage } from "./ReasonReviewLegacy";

type TestUser = { id: number; role: string; status: string; accountType: string; birthDate: string };
const auth = vi.hoisted(() => ({ user: null as unknown, loading: false, isAdmin: true }));
vi.mock("../auth", () => ({ useAuth: () => auth }));
vi.mock("../api/adminUsers", async (actual) => ({
  ...(await actual<typeof import("../api/adminUsers")>()),
  AdminUsers: { legacyReasons: vi.fn(), legacyReasonsPage: vi.fn(), mintAttempt: vi.fn(), settleAttempt: vi.fn(), saveLegacyReasons: vi.fn() },
}));

const TITLE = "Deleted accounts with reasons to review";
const INTRO = "These deleted accounts still have contact-request reasons nobody has reviewed. Choose for each whether Fold keeps the text or blanks it.";
const SEARCH_LIMIT = "Fold lists only requests this person made or was asked about. Reasons in other people's requests that mention them aren't listed here.";
const STOPPED = "You stopped checking, so this list may not show every deleted account.";
const EMPTY = "No deleted accounts have reasons left to review.";
const BUSY = ERROR_COPY.reason_review_busy;
const NOT_ALLOWED = "Only an adult admin can review these reasons. Ask another adult admin to review them.";

const admin: TestUser = { id: 1, role: "admin", status: "active", accountType: "adult", birthDate: "1980-01-01" };
// Every account carries the same former name: only the id may tell them apart.
const account = (userId: string, ...requestIds: string[]): LegacyDiscoveryAccount => ({ userId, name: "Ann Smith", items: requestIds.map((requestId) => ({ requestId })) });
const unit = (next: string | null, ...accounts: LegacyDiscoveryAccount[]): LegacyReasonDiscovery => ({ accounts, next });
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((a, b) => {
    resolve = a;
    reject = b;
  });
  return { promise, resolve, reject };
}
// One undecided reason on a deleted account's page. The hash is 64 hex digits, as the server sends.
const hash = (digit: string) => digit.repeat(64);
const reason = (requestId: string, partyRef: string, over: Partial<ErasureReasonItem> = {}): ErasureReasonItem => ({
  requestId,
  reviewHash: hash("a"),
  role: "requester",
  purpose: "contact",
  status: "pending",
  createdAt: "2026-01-02T03:04:05Z",
  counterpartName: "Deleted account",
  counterpartDeleted: false,
  partyRef,
  reviewerIsParty: false,
  reason: `reason text ${requestId}`,
  ...over,
});
const walk = vi.mocked(AdminUsers.legacyReasons);
const cursors = () => walk.mock.calls.map(([after]) => after);

function mount(path = "/admin/deleted-reasons") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/admin/deleted-reasons" element={<ReasonReviewLegacyPage />} />
      </Routes>
    </MemoryRouter>,
  );
}
const labels = () => screen.queryAllByRole("link").map((a) => a.textContent);

beforeEach(() => {
  auth.user = admin;
  auth.loading = false;
  auth.isAdmin = true;
  for (const fn of Object.values(AdminUsers)) vi.mocked(fn).mockReset();
  reloadSucceeded(reloadStarted()); // restore_detected is process-wide: start each test clean
});
afterEach(() => vi.restoreAllMocks());

describe("who may open the page", () => {
  it.each([
    ["a member", { ...admin, role: "member" }, false],
    ["an admin who is not active", { ...admin, status: "disabled" }, true],
    ["a teen admin", { ...admin, accountType: "teen" }, true],
  ])("shows %s only the admins-only page and asks for nothing", (_who, user, isAdmin) => {
    auth.user = user;
    auth.isAdmin = isAdmin;
    mount();
    expect(screen.getByRole("heading", { level: 1, name: TITLE })).toBeInTheDocument();
    expect(screen.getByText("This page is for church admins only.")).toBeInTheDocument();
    expect(walk).not.toHaveBeenCalled();
    expect(screen.queryByText(INTRO)).not.toBeInTheDocument();
  });

  it("shows nothing and asks for nothing while the session loads or when signed out", () => {
    auth.loading = true;
    const view = mount();
    expect(view.container).toBeEmptyDOMElement();
    view.unmount();
    auth.loading = false;
    auth.user = null;
    const out = mount();
    expect(out.container).toBeEmptyDOMElement();
    expect(walk).not.toHaveBeenCalled();
  });
});

describe("the discovery walk", () => {
  it("walks every unit, an empty one with a next included, from the exact cursors, and merges by id", async () => {
    walk
      .mockResolvedValueOnce(unit("10", account("41", "1"), account("52", "2")))
      .mockResolvedValueOnce(unit("20"))
      .mockResolvedValueOnce(unit(null, account("52", "3"), account("63", "4")));
    mount();
    expect(screen.getByRole("heading", { level: 1, name: TITLE })).toBeInTheDocument();
    expect(screen.getByText(INTRO)).toBeInTheDocument();
    expect(screen.getByText(SEARCH_LIMIT)).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Deleted account · ref 63" })).toBeInTheDocument();
    expect(cursors()).toEqual([null, "10", "20"]);
    // Account 52 continued on the last unit and is listed once, in first-seen order.
    expect(labels()).toEqual(["Deleted account · ref 41", "Deleted account · ref 52", "Deleted account · ref 63"]);
    expect(screen.getByRole("link", { name: "Deleted account · ref 52" })).toHaveAttribute("href", "/admin/deleted-reasons?account=52");
    expect(screen.queryByText("Checking…")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Stop" })).not.toBeInTheDocument();
    expect(screen.queryByText(EMPTY)).not.toBeInTheDocument();
  });

  it("labels an account by its id alone and never shows a former name", async () => {
    walk.mockResolvedValueOnce(unit(null, account("41", "1"), account("52", "2")));
    mount();
    await screen.findByRole("link", { name: "Deleted account · ref 52" });
    expect(labels()).toEqual(["Deleted account · ref 41", "Deleted account · ref 52"]);
    expect(document.body).not.toHaveTextContent("Ann Smith");
  });

  it("keeps two full-width ids apart and exact", async () => {
    walk.mockResolvedValueOnce(unit(null, account("9007199254740992", "1"), account("9007199254740993", "2")));
    mount();
    const link = await screen.findByRole("link", { name: "Deleted account · ref 9007199254740993" });
    expect(link).toHaveAttribute("href", "/admin/deleted-reasons?account=9007199254740993");
    expect(labels()).toEqual(["Deleted account · ref 9007199254740992", "Deleted account · ref 9007199254740993"]);
  });

  it("says it is checking with a Stop, and shows the empty state only when the walk reached its end with nothing", async () => {
    const second = deferred<LegacyReasonDiscovery>();
    walk.mockResolvedValueOnce(unit("10")).mockReturnValueOnce(second.promise);
    mount();
    expect(screen.getByRole("status")).toHaveTextContent("Checking…");
    expect(await screen.findByRole("button", { name: "Stop" })).toBeInTheDocument();
    await waitFor(() => expect(walk).toHaveBeenCalledTimes(2));
    expect(screen.queryByText(EMPTY)).not.toBeInTheDocument();
    await act(async () => second.resolve(unit(null)));
    const empty = await screen.findByText(EMPTY);
    expect(empty.closest("div")?.className).not.toMatch(/danger/);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(cursors()).toEqual([null, "10"]);
  });
});

describe("Stop and Try again", () => {
  it("drops the unit in flight on Stop, keeps what was found, and resumes from the last good cursor", async () => {
    const second = deferred<LegacyReasonDiscovery>();
    walk
      .mockResolvedValueOnce(unit("10", account("41", "1")))
      .mockReturnValueOnce(second.promise)
      .mockResolvedValueOnce(unit(null, account("52", "2")));
    mount();
    await screen.findByRole("link", { name: "Deleted account · ref 41" });
    await waitFor(() => expect(walk).toHaveBeenCalledTimes(2));
    await userEvent.click(screen.getByRole("button", { name: "Stop" }));
    expect(screen.getByRole("status")).toHaveTextContent(STOPPED);
    expect(screen.getByRole("status").parentElement).toHaveFocus();
    // The dropped unit answers late: nothing from it is shown or followed.
    await act(async () => second.resolve(unit("30", account("99", "9"))));
    expect(labels()).toEqual(["Deleted account · ref 41"]);
    expect(walk).toHaveBeenCalledTimes(2);

    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    await screen.findByRole("link", { name: "Deleted account · ref 52" });
    // The second unit's own cursor "10" is asked for again; the answer that was dropped never advanced it.
    expect(cursors()).toEqual([null, "10", "10"]);
    expect(labels()).toEqual(["Deleted account · ref 41", "Deleted account · ref 52"]);
    expect(screen.queryByText(STOPPED)).not.toBeInTheDocument();
  });

  it("never claims there is nothing when stopped with nothing found", async () => {
    const first = deferred<LegacyReasonDiscovery>();
    walk.mockReturnValueOnce(first.promise);
    mount();
    await userEvent.click(screen.getByRole("button", { name: "Stop" }));
    await act(async () => first.resolve(unit(null)));
    expect(screen.getByRole("status")).toHaveTextContent(STOPPED);
    expect(screen.queryByText(EMPTY)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("ignores an old answer that arrives after Try again started a new walk", async () => {
    const old = deferred<LegacyReasonDiscovery>();
    const fresh = deferred<LegacyReasonDiscovery>();
    walk.mockReturnValueOnce(old.promise).mockReturnValueOnce(fresh.promise);
    mount();
    await userEvent.click(screen.getByRole("button", { name: "Stop" }));
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    await act(async () => old.resolve(unit("30", account("99", "9"))));
    expect(walk).toHaveBeenCalledTimes(2);
    expect(labels()).toEqual([]);
    expect(screen.getByRole("status")).toHaveTextContent("Checking…");
    await act(async () => fresh.resolve(unit(null, account("41", "1"))));
    expect(labels()).toEqual(["Deleted account · ref 41"]);
  });

  it("drops the unit in flight when the page is left, and asks for nothing more", async () => {
    const first = deferred<LegacyReasonDiscovery>();
    walk.mockReturnValueOnce(first.promise);
    const view = mount();
    view.unmount();
    await act(async () => first.resolve(unit("10", account("41", "1"))));
    expect(walk).toHaveBeenCalledTimes(1);
  });
});

describe("a failed unit", () => {
  it.each([
    ["a busy server", new ApiError(503, "private server text", { code: "reason_review_busy" })],
    ["a malformed answer", new ApiError(502, "unexpected answer to a deleted-accounts list")],
    ["a refused cursor", new ApiError(400, "invalid cursor")],
    ["a gateway page", new ApiError(500, "<html>", {}, true)],
    ["a dropped connection", new TypeError("Failed to fetch")],
  ])("after %s says the list couldn't load and retries the same cursor, keeping what was found", async (_name, failure) => {
    walk
      .mockResolvedValueOnce(unit("10", account("41", "1")))
      .mockRejectedValueOnce(failure)
      .mockResolvedValueOnce(unit(null, account("52", "2")));
    mount();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(BUSY);
    expect(alert.parentElement).toHaveFocus();
    expect(document.body).not.toHaveTextContent(/unexpected answer|private server text|Failed to fetch|invalid cursor|<html>/);
    expect(labels()).toEqual(["Deleted account · ref 41"]);
    expect(screen.queryByText(EMPTY)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Stop" })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    await screen.findByRole("link", { name: "Deleted account · ref 52" });
    expect(cursors()).toEqual([null, "10", "10"]);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("fails closed rather than loop on a cursor that does not advance, keeping the earlier accounts", async () => {
    walk.mockResolvedValueOnce(unit("10", account("41", "1"))).mockResolvedValueOnce(unit("10", account("52", "2")));
    mount();
    expect(await screen.findByRole("alert")).toHaveTextContent(BUSY);
    expect(walk).toHaveBeenCalledTimes(2);
    expect(labels()).toEqual(["Deleted account · ref 41"]);
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("fails closed on a cursor it already asked for earlier in the walk", async () => {
    walk
      .mockResolvedValueOnce(unit("10", account("41", "1")))
      .mockResolvedValueOnce(unit("20", account("52", "2")))
      .mockResolvedValueOnce(unit("10", account("63", "3")));
    mount();
    expect(await screen.findByRole("alert")).toHaveTextContent(BUSY);
    expect(walk).toHaveBeenCalledTimes(3);
    expect(labels()).toEqual(["Deleted account · ref 41", "Deleted account · ref 52"]);
  });

  it.each([
    ["a coded 403", new ApiError(403, "forbidden", { code: "reason_review_not_allowed" }), NOT_ALLOWED],
    ["a plain 403", new ApiError(403, "forbidden"), NOT_ALLOWED],
    ["a 401", new ApiError(401, "signed out"), ERROR_COPY.signed_out],
  ])("locks on %s with no Try again and no list", async (_name, failure, copy) => {
    walk.mockResolvedValueOnce(unit("10", account("41", "1"))).mockRejectedValueOnce(failure);
    mount();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(copy);
    expect(alert.parentElement).toHaveFocus();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(screen.queryByText(EMPTY)).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(/delete again|delete this account|delete request/);
  });

});

describe("opening one account", () => {
  async function openFirst() {
    walk.mockResolvedValue(unit(null, account("9007199254740993", "1"), account("52", "2")));
    mount();
    await userEvent.click(await screen.findByRole("link", { name: "Deleted account · ref 9007199254740993" }));
  }

  it("shows the account heading from the id and the search limit, without the plural intro", async () => {
    vi.mocked(AdminUsers.legacyReasonsPage).mockResolvedValue({ items: [], next: null });
    await openFirst();
    expect(screen.getByRole("heading", { level: 1, name: "Deleted account · ref 9007199254740993" })).toBeInTheDocument();
    expect(screen.getByText(SEARCH_LIMIT)).toBeInTheDocument();
    expect(screen.queryByText(INTRO)).not.toBeInTheDocument();
    expect(AdminUsers.mintAttempt).not.toHaveBeenCalled();
    expect(document.body).not.toHaveTextContent("Ann Smith");
  });

  it("goes back to the list with the walk intact and not started again", async () => {
    vi.mocked(AdminUsers.legacyReasonsPage).mockResolvedValue({ items: [], next: null });
    await openFirst();
    await userEvent.click(screen.getByRole("link", { name: TITLE }));
    expect(await screen.findByRole("link", { name: "Deleted account · ref 52" })).toBeInTheDocument();
    expect(screen.getByText(INTRO)).toBeInTheDocument();
    expect(walk).toHaveBeenCalledTimes(1);
  });

  it("keeps walking while an account is open, and shows the result on return", async () => {
    vi.mocked(AdminUsers.legacyReasonsPage).mockResolvedValue({ items: [reason("1", "41")], next: null });
    const second = deferred<LegacyReasonDiscovery>();
    walk.mockResolvedValueOnce(unit("10", account("41", "1"))).mockReturnValueOnce(second.promise);
    mount();
    await userEvent.click(await screen.findByRole("link", { name: "Deleted account · ref 41" }));
    await act(async () => second.resolve(unit(null, account("52", "2"))));
    await userEvent.click(screen.getByRole("link", { name: TITLE }));
    expect(labels()).toEqual(["Deleted account · ref 41", "Deleted account · ref 52"]);
    expect(walk).toHaveBeenCalledTimes(2);
  });

  it.each(["abc", "0", "007", "-5", "1.5", "1e3", " 7", "9223372036854775808", "99999999999999999999"])(
    "says an account that is not a canonical id (%j) no longer exists and sends nothing for it",
    (bad) => {
      walk.mockResolvedValue(unit(null));
      mount(`/admin/deleted-reasons?account=${encodeURIComponent(bad)}`);
      expect(screen.getByText("This account no longer exists.")).toBeInTheDocument();
      expect(screen.getByRole("heading", { level: 1, name: "Deleted account" })).toBeInTheDocument();
      expect(screen.getByRole("link", { name: TITLE })).toHaveAttribute("href", "/admin/deleted-reasons");
      expect(document.body).not.toHaveTextContent(bad.trim());
      expect(walk).not.toHaveBeenCalled();
      expect(AdminUsers.legacyReasonsPage).not.toHaveBeenCalled();
      expect(AdminUsers.mintAttempt).not.toHaveBeenCalled();
      expect(AdminUsers.settleAttempt).not.toHaveBeenCalled();
      expect(AdminUsers.saveLegacyReasons).not.toHaveBeenCalled();
    },
  );

  it("opens a deep link to a valid account directly, with the largest signed id intact", () => {
    walk.mockResolvedValue(unit(null));
    vi.mocked(AdminUsers.legacyReasonsPage).mockResolvedValue({ items: [], next: null });
    mount("/admin/deleted-reasons?account=9223372036854775807");
    const heading = screen.getByRole("heading", { level: 1 });
    expect(within(heading.parentElement as HTMLElement).getByText("Deleted account · ref 9223372036854775807")).toBe(heading);
  });
});

// ---- One account: choosing, saving and settling (T-302 S8) ----

const pageCall = vi.mocked(AdminUsers.legacyReasonsPage);
const mint = vi.mocked(AdminUsers.mintAttempt);
const put = vi.mocked(AdminUsers.saveLegacyReasons);
const settleCall = vi.mocked(AdminUsers.settleAttempt);
const X1 = "ab".repeat(40);
const X2 = "cd".repeat(40);
const SAVED = "Saved.";
const NOT_APPLIED = "These choices weren't saved. Reload to see what's left.";
const HISTORY = "Fold can't tell whether your earlier save finished. Reload to see what's left.";
const CHECKING = "Checking what happened to this save…";
const STILL = "Still checking. Fold can't tell yet whether your choices were saved. Check again in a moment.";
const CHANGED = "Some reasons changed since you loaded them. Check the marked ones, then save again.";
const ACCOUNT_EMPTY = "This account has no reasons left to review.";
const PRE_SAVE = "Saving blanks the text you chose right away. It can't be brought back in Fold. Backups made before now still contain it.";
const DELETION_WORDING = /delete again|delete this account|delete request|Ask another admin to delete/;
const NOT_SAVED_WORDING = /weren't saved|wasn't saved|not saved|nothing was saved/i;

const pageOf = (...items: ErasureReasonItem[]): ErasureReasonPage => ({ items, next: null, reviewRequired: true });
// Three reasons on account 52 with distinct hashes.
const three = () => [reason("1", "52", { reviewHash: hash("1") }), reason("2", "52", { reviewHash: hash("2") }), reason("3", "52", { reviewHash: hash("3"), purpose: "group_add" })];

function mountAccount(id = "52") {
  return render(
    <MemoryRouter initialEntries={[`/admin/deleted-reasons?account=${id}`]}>
      <Link to="/admin/deleted-reasons?account=63">switch to 63</Link>
      <Routes>
        <Route path="/admin/deleted-reasons" element={<ReasonReviewLegacyPage />} />
      </Routes>
    </MemoryRouter>,
  );
}
const saveButton = () => screen.getByRole("button", { name: "Save" });
const radio = (ordinal: number, total: number, label: "Keep text" | "Blank text") =>
  within(screen.getByRole("group", { name: `Reason ${ordinal} of ${total}` })).getByRole("radio", { name: label });
async function choose(ordinal: number, total: number, label: "Keep text" | "Blank text") {
  await userEvent.click(radio(ordinal, total, label));
}
async function chooseAll(total: number, label: "Keep text" | "Blank text" = "Keep text") {
  for (let i = 1; i <= total; i++) await choose(i, total, label);
}
async function pressSave() {
  await userEvent.click(saveButton());
}
// Opens account 52 with `items`, every one chosen as Keep, ready to save.
async function readyToSave(items: ErasureReasonItem[] = three()) {
  pageCall.mockResolvedValueOnce(pageOf(...items));
  const view = mountAccount();
  await screen.findByRole("group", { name: `Reason 1 of ${items.length}` });
  await chooseAll(items.length, "Keep text");
  return view;
}
const expectedDecisions = (items: ErasureReasonItem[], choice: "keep" | "remove" = "keep") => items.map((i) => ({ requestId: i.requestId, reviewHash: i.reviewHash, choice }));
const checkedRadios = () => screen.queryAllByRole("radio", { checked: true });
const noDeletionWording = () => expect(document.body).not.toHaveTextContent(DELETION_WORDING);

describe("an account's reasons", () => {
  beforeEach(() => {
    walk.mockResolvedValue(unit(null, account("52", "1"), account("63", "1")));
  });

  it("asks for its page once, from the start, and shows every reason with nothing chosen and no bulk control", async () => {
    pageCall.mockResolvedValue(pageOf(...three()));
    mountAccount();
    await screen.findByRole("group", { name: "Reason 1 of 3" });
    expect(pageCall.mock.calls).toEqual([["52", null]]);
    expect(screen.getByRole("heading", { level: 1, name: "Deleted account · ref 52" })).toBeInTheDocument();
    expect(screen.getByText(SEARCH_LIMIT)).toBeInTheDocument();
    expect(checkedRadios()).toHaveLength(0);
    expect(screen.getAllByText("Not chosen yet")).toHaveLength(3);
    expect(saveButton()).toBeDisabled();
    expect(screen.getByText("Choose Keep text or Blank text for 3 more reasons.")).toBeInTheDocument();
    expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual(["Save"]);
    expect(mint).not.toHaveBeenCalled();
  });

  it("turns Save on only when every reason is chosen, and shows the blanking warning only while one is blanked", async () => {
    pageCall.mockResolvedValue(pageOf(...three()));
    mountAccount();
    await screen.findByRole("group", { name: "Reason 1 of 3" });
    await choose(1, 3, "Keep text");
    await choose(2, 3, "Keep text");
    expect(saveButton()).toBeDisabled();
    expect(screen.getByText("Choose Keep text or Blank text for 1 more reason.")).toBeInTheDocument();
    await choose(3, 3, "Keep text");
    expect(saveButton()).toBeEnabled();
    expect(screen.queryByText(PRE_SAVE)).not.toBeInTheDocument();
    await choose(2, 3, "Blank text");
    const warning = screen.getByText(PRE_SAVE);
    expect(warning.className).not.toMatch(/danger/);
    expect(saveButton()).toBeEnabled();
    await choose(2, 3, "Keep text");
    expect(screen.queryByText(PRE_SAVE)).not.toBeInTheDocument();
  });

  it("sends exactly the loaded reasons, in page order, with their hashes and the choice made for each, under one fresh id", async () => {
    const items = three();
    pageCall.mockResolvedValueOnce(pageOf(...items)).mockResolvedValue(pageOf());
    mint.mockResolvedValueOnce({ attemptId: X1 });
    put.mockResolvedValueOnce({ kept: 2, removed: 1 });
    mountAccount();
    await screen.findByRole("group", { name: "Reason 1 of 3" });
    await choose(1, 3, "Keep text");
    await choose(2, 3, "Blank text");
    await choose(3, 3, "Keep text");
    await pressSave();
    expect(await screen.findByText(SAVED)).toBeInTheDocument();
    expect(mint.mock.calls).toEqual([["52", "legacy_reason_save"]]);
    expect(put.mock.calls).toEqual([
      ["52", X1, [
        { requestId: "1", reviewHash: hash("1"), choice: "keep" },
        { requestId: "2", reviewHash: hash("2"), choice: "remove" },
        { requestId: "3", reviewHash: hash("3"), choice: "keep" },
      ]],
    ]);
    expect(settleCall).not.toHaveBeenCalled();
    // The counts are checked by the client and never shown; the id and hashes never reach the page.
    expect(document.body).not.toHaveTextContent(/kept|blanked/i);
    expect(document.body.innerHTML).not.toContain(X1);
    expect(document.body.innerHTML).not.toContain(hash("1"));
    noDeletionWording();
  });

  it("after Saved reloads the page, and what it shows is new and unchosen with no changed mark", async () => {
    const next = [reason("4", "52", { reviewHash: hash("4") }), reason("5", "52", { reviewHash: hash("5") })];
    pageCall.mockResolvedValueOnce(pageOf(...three())).mockResolvedValueOnce(pageOf(...next));
    mint.mockResolvedValueOnce({ attemptId: X1 }).mockResolvedValueOnce({ attemptId: X2 });
    put.mockResolvedValue({ kept: 3, removed: 0 });
    mountAccount();
    await screen.findByRole("group", { name: "Reason 1 of 3" });
    await chooseAll(3, "Keep text");
    await pressSave();
    await screen.findByRole("group", { name: "Reason 1 of 2" });
    expect(pageCall.mock.calls).toEqual([["52", null], ["52", null]]);
    expect(screen.getByText(SAVED)).toBeInTheDocument();
    expect(checkedRadios()).toHaveLength(0);
    expect(screen.queryByText(/Changed since you looked/)).not.toBeInTheDocument();
    expect(saveButton()).toBeDisabled();
    // The next press mints its own id and sends only what is now shown.
    await chooseAll(2, "Keep text");
    await pressSave();
    await waitFor(() => expect(put).toHaveBeenCalledTimes(2));
    expect(mint).toHaveBeenCalledTimes(2);
    expect(put.mock.calls[1]).toEqual(["52", X2, expectedDecisions(next)]);
  });

  it("says there is nothing left when the page is empty, and the account leaves the list", async () => {
    pageCall.mockResolvedValue(pageOf());
    walk.mockResolvedValue(unit(null, account("52", "1"), account("63", "1")));
    const view = render(
      <MemoryRouter initialEntries={["/admin/deleted-reasons"]}>
        <Routes>
          <Route path="/admin/deleted-reasons" element={<ReasonReviewLegacyPage />} />
        </Routes>
      </MemoryRouter>,
    );
    await userEvent.click(await screen.findByRole("link", { name: "Deleted account · ref 52" }));
    const empty = await screen.findByText(ACCOUNT_EMPTY);
    expect(empty.closest("div")?.className).not.toMatch(/danger/);
    expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("link", { name: TITLE }));
    expect(labels()).toEqual(["Deleted account · ref 63"]);
    expect(walk).toHaveBeenCalledTimes(1);
    view.unmount();
  });

  it("never shows a former name, the account's id as a name or a hash, even for a deleted counterpart", async () => {
    pageCall.mockResolvedValueOnce(
      pageOf(reason("1", "52", { counterpartName: "Bob Former", counterpartDeleted: true, counterpartRef: "41", reviewHash: hash("9") })),
    );
    mountAccount();
    await screen.findByRole("group", { name: "Reason 1 of 1" });
    expect(document.body).toHaveTextContent("Deleted account · ref 41");
    expect(document.body).not.toHaveTextContent(/Bob Former|Ann Smith/);
    expect(document.body.innerHTML).not.toContain(hash("9"));
    noDeletionWording();
  });
});

describe("a page that cannot be trusted or read", () => {
  beforeEach(() => {
    walk.mockResolvedValue(unit(null, account("52", "1")));
  });

  it.each([
    ["a reason about another account", () => [reason("1", "52"), reason("2", "99")]],
    ["a reason with no account", () => [reason("1", "52"), { ...reason("2", "52"), partyRef: undefined } as ErasureReasonItem]],
    ["more than 100 reasons", () => Array.from({ length: 101 }, (_, i) => reason(String(i + 1), "52"))],
    ["a repeated request", () => [reason("1", "52"), reason("1", "52")]],
  ])("fails closed on %s, shows no reason and asks again from the same request", async (_name, bad) => {
    pageCall.mockResolvedValueOnce(pageOf(...bad())).mockResolvedValueOnce(pageOf(reason("7", "52")));
    mountAccount();
    expect(await screen.findByRole("alert")).toHaveTextContent(BUSY);
    expect(screen.queryByRole("radio")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(/reason text/);
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    await screen.findByRole("group", { name: "Reason 1 of 1" });
    expect(pageCall.mock.calls).toEqual([["52", null], ["52", null]]);
  });

  it("accepts exactly 100 reasons", async () => {
    pageCall.mockResolvedValueOnce(pageOf(...Array.from({ length: 100 }, (_, i) => reason(String(i + 1), "52"))));
    mountAccount();
    await screen.findByRole("group", { name: "Reason 1 of 100" });
    expect(screen.getAllByRole("group")).toHaveLength(100);
  });

  it.each([
    ["a busy server", new ApiError(503, "private server text", { code: "reason_review_busy" })],
    ["a malformed answer", new ApiError(502, "unexpected answer to a deleted account's reasons")],
    ["a refused cursor", new ApiError(400, "invalid cursor")],
    ["a gateway page", new ApiError(500, "<html>", {}, true)],
    ["a dropped connection", new TypeError("Failed to fetch")],
  ])("after %s says the reasons couldn't load, never the wrapper's words, and retries the same request", async (_name, failure) => {
    pageCall.mockRejectedValueOnce(failure).mockResolvedValueOnce(pageOf(reason("7", "52")));
    mountAccount();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(BUSY);
    expect(alert.parentElement).toHaveFocus();
    expect(document.body).not.toHaveTextContent(/unexpected answer|private server text|Failed to fetch|invalid cursor|<html>/);
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    await screen.findByRole("group", { name: "Reason 1 of 1" });
    expect(pageCall.mock.calls).toEqual([["52", null], ["52", null]]);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it.each([
    ["a coded 403", new ApiError(403, "forbidden", { code: "reason_review_not_allowed" }), NOT_ALLOWED],
    ["a plain 403", new ApiError(403, "forbidden"), NOT_ALLOWED],
    ["a 401", new ApiError(401, "signed out"), ERROR_COPY.signed_out],
  ])("locks on %s with no Try again", async (_name, failure, copy) => {
    pageCall.mockRejectedValueOnce(failure);
    mountAccount();
    expect(await screen.findByRole("alert")).toHaveTextContent(copy);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.queryByRole("radio")).not.toBeInTheDocument();
    noDeletionWording();
  });

  it("says an account the server does not know no longer exists, and does not offer to ask again", async () => {
    pageCall.mockRejectedValueOnce(new ApiError(404, "user not found"));
    mountAccount();
    expect(await screen.findByText("This account no longer exists.")).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: TITLE })).toHaveAttribute("href", "/admin/deleted-reasons");
  });
});

describe("pressing Save", () => {
  beforeEach(() => {
    walk.mockResolvedValue(unit(null, account("52", "1"), account("63", "1")));
  });

  it("holds everything still while the save is in flight, and sends the choices frozen at the press", async () => {
    const items = three();
    const inflight = deferred<{ kept: number; removed: number }>();
    await readyToSave(items);
    mint.mockResolvedValueOnce({ attemptId: X1 });
    put.mockReturnValueOnce(inflight.promise);
    pageCall.mockResolvedValue(pageOf());
    await pressSave();
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    expect(saveButton()).toBeDisabled();
    for (let i = 1; i <= 3; i++) {
      expect(radio(i, 3, "Keep text")).toBeDisabled();
      expect(radio(i, 3, "Blank text")).toBeDisabled();
    }
    await userEvent.click(radio(2, 3, "Blank text"));
    expect(radio(2, 3, "Keep text")).toBeChecked();
    await userEvent.click(saveButton());
    expect(mint).toHaveBeenCalledTimes(1);
    expect(put.mock.calls[0]).toEqual(["52", X1, expectedDecisions(items)]);
    expect(document.body).not.toHaveTextContent(NOT_SAVED_WORDING);
    await act(async () => inflight.resolve({ kept: 3, removed: 0 }));
    expect(await screen.findByText(SAVED)).toBeInTheDocument();
  });

  it("mints once for a double click", async () => {
    await readyToSave();
    mint.mockResolvedValue({ attemptId: X1 });
    put.mockResolvedValue({ kept: 3, removed: 0 });
    pageCall.mockResolvedValue(pageOf());
    const button = saveButton();
    act(() => {
      button.click();
      button.click();
    });
    await screen.findByText(SAVED);
    expect(mint).toHaveBeenCalledTimes(1);
    expect(put).toHaveBeenCalledTimes(1);
  });

  it("never sends or mints while a reason has no choice", async () => {
    pageCall.mockResolvedValueOnce(pageOf(...three()));
    mountAccount();
    await screen.findByRole("group", { name: "Reason 1 of 3" });
    await choose(1, 3, "Keep text");
    await userEvent.click(saveButton());
    expect(mint).not.toHaveBeenCalled();
    expect(put).not.toHaveBeenCalled();
  });

  describe("an answer that ends the attempt without a save", () => {
    it.each([
      ["legacy_save_timed_out", new ApiError(503, "x", { code: "legacy_save_timed_out" }), ERROR_COPY.legacy_save_timed_out],
      ["legacy_save_busy", new ApiError(409, "x", { code: "legacy_save_busy" }), ERROR_COPY.legacy_save_busy],
      ["admin_account_busy", new ApiError(409, "x", { code: "admin_account_busy" }), ERROR_COPY.admin_account_busy],
      ["attempt_busy", new ApiError(503, "x", { code: "attempt_busy" }), ERROR_COPY.attempt_busy],
      ["a 400 with a message only", new ApiError(400, "invalid decisions"), "That didn't work. Please try again."],
    ])("%s keeps the choices, settles nothing, and the next press mints a fresh id", async (_name, failure, copy) => {
      const items = three();
      await readyToSave(items);
      mint.mockResolvedValueOnce({ attemptId: X1 }).mockResolvedValueOnce({ attemptId: X2 });
      put.mockRejectedValueOnce(failure).mockResolvedValueOnce({ kept: 3, removed: 0 });
      pageCall.mockResolvedValue(pageOf());
      await pressSave();
      const alert = await screen.findByRole("alert");
      expect(alert).toHaveTextContent(copy);
      expect(document.body).not.toHaveTextContent(/private|invalid decisions/);
      expect(settleCall).not.toHaveBeenCalled();
      expect(checkedRadios()).toHaveLength(3);
      expect(saveButton()).toBeEnabled();
      expect(radio(1, 3, "Keep text")).toBeEnabled();
      await pressSave();
      await screen.findByText(SAVED);
      expect(mint).toHaveBeenCalledTimes(2);
      expect(put.mock.calls.map(([, id, decisions]) => [id, decisions])).toEqual([[X1, expectedDecisions(items)], [X2, expectedDecisions(items)]]);
      noDeletionWording();
    });

    it.each([
      ["a coded 403", new ApiError(403, "x", { code: "reason_review_not_allowed" }), NOT_ALLOWED],
      ["a plain 403", new ApiError(403, "x"), ERROR_COPY.not_allowed],
      ["a 401", new ApiError(401, "x"), ERROR_COPY.signed_out],
      ["a 404", new ApiError(404, "x"), "This account no longer exists."],
    ])("%s locks the page: no reasons, no Save, nothing to settle", async (_name, failure, copy) => {
      await readyToSave();
      mint.mockResolvedValueOnce({ attemptId: X1 });
      put.mockRejectedValueOnce(failure);
      await pressSave();
      expect(await screen.findByRole("alert")).toHaveTextContent(copy);
      expect(screen.queryByRole("radio")).not.toBeInTheDocument();
      expect(screen.queryByRole("button")).not.toBeInTheDocument();
      expect(settleCall).not.toHaveBeenCalled();
      expect(mint).toHaveBeenCalledTimes(1);
      expect(document.body).not.toHaveTextContent(/reason text/);
      noDeletionWording();
    });

    it("locks on a restore until a reload that starts afterwards succeeds, and leaves the process-wide hold for the deletes", async () => {
      await readyToSave();
      mint.mockResolvedValueOnce({ attemptId: X1 });
      put.mockRejectedValueOnce(new ApiError(503, "x", { code: "restore_detected" }));
      await pressSave();
      expect(await screen.findByRole("alert")).toHaveTextContent(ERROR_COPY.restore_detected);
      expect(needsRestoreReload()).toBe(true);
      expect(saveButton()).toBeDisabled();
      expect(radio(1, 3, "Keep text")).toBeDisabled();
      expect(settleCall).not.toHaveBeenCalled();
      pageCall.mockResolvedValueOnce(pageOf(...three()));
      await userEvent.click(screen.getByRole("button", { name: "Reload" }));
      await screen.findByRole("group", { name: "Reason 1 of 3" });
      expect(pageCall).toHaveBeenCalledTimes(2);
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      // Nothing carries over a restore: the choices are made again.
      expect(checkedRadios()).toHaveLength(0);
      expect(saveButton()).toBeDisabled();
      await chooseAll(3, "Keep text");
      expect(saveButton()).toBeEnabled();
      expect(needsRestoreReload()).toBe(true);
    });

    it("stays locked after a restore when the reload fails, and unlocks on the next that works", async () => {
      await readyToSave();
      mint.mockResolvedValueOnce({ attemptId: X1 });
      put.mockRejectedValueOnce(new ApiError(503, "x", { code: "restore_detected" }));
      await pressSave();
      await screen.findByText(ERROR_COPY.restore_detected);
      pageCall.mockRejectedValueOnce(new TypeError("Failed to fetch")).mockResolvedValueOnce(pageOf(...three()));
      await userEvent.click(screen.getByRole("button", { name: "Reload" }));
      const alerts = await screen.findAllByRole("alert");
      expect(alerts.map((a) => a.textContent)).toContain(BUSY);
      expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
      await userEvent.click(screen.getByRole("button", { name: "Try again" }));
      await screen.findByRole("group", { name: "Reason 1 of 3" });
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });
  });

  describe("minting the attempt id fails", () => {
    it.each([
      ["a busy server", new ApiError(503, "x", { code: "attempt_busy" }), ERROR_COPY.attempt_busy],
      ["a malformed mint", new ApiError(502, "unexpected answer to a mint"), "That didn't work. Please try again in a minute."],
      ["a dropped connection", new TypeError("Failed to fetch"), "That didn't work."],
      ["an id it cannot carry", new Error("not a usable account id"), "That didn't work."],
    ])("%s sends no PUT, settles nothing and leaves Save on", async (_name, failure, copy) => {
      await readyToSave();
      mint.mockRejectedValueOnce(failure);
      await pressSave();
      expect(await screen.findByRole("alert")).toHaveTextContent(copy);
      expect(put).not.toHaveBeenCalled();
      expect(settleCall).not.toHaveBeenCalled();
      expect(saveButton()).toBeEnabled();
      expect(checkedRadios()).toHaveLength(3);
      expect(document.body).not.toHaveTextContent(/unexpected answer|Failed to fetch|not a usable/);
    });

    it.each([
      ["a 401", new ApiError(401, "x"), ERROR_COPY.signed_out],
      ["a 403", new ApiError(403, "x"), ERROR_COPY.not_allowed],
    ])("%s locks the page without a PUT", async (_name, failure, copy) => {
      await readyToSave();
      mint.mockRejectedValueOnce(failure);
      await pressSave();
      expect(await screen.findByRole("alert")).toHaveTextContent(copy);
      expect(put).not.toHaveBeenCalled();
      expect(screen.queryByRole("button")).not.toBeInTheDocument();
    });

    it("locks on a restore with a Reload", async () => {
      await readyToSave();
      mint.mockRejectedValueOnce(new ApiError(503, "x", { code: "restore_detected" }));
      await pressSave();
      expect(await screen.findByRole("alert")).toHaveTextContent(ERROR_COPY.restore_detected);
      expect(needsRestoreReload()).toBe(true);
      expect(put).not.toHaveBeenCalled();
      expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
    });
  });
});

describe("an answer that does not say what happened", () => {
  beforeEach(() => {
    walk.mockResolvedValue(unit(null, account("52", "1")));
  });

  const uncertain: Array<[string, unknown, string]> = [
    ["legacy_save_outcome_unknown", new ApiError(500, "x", { code: "legacy_save_outcome_unknown" }), ERROR_COPY.legacy_save_outcome_unknown],
    ["attempt_closed", new ApiError(409, "x", { code: "attempt_closed" }), ERROR_COPY.attempt_closed],
    ["attempt_expired", new ApiError(409, "x", { code: "attempt_expired" }), "This save was too old to run, so Fold didn't run it. Fold is checking what happened."],
    ["a 2xx that is not the counts", new Error("unexpected answer to a save"), CHECKING],
    ["a dropped connection", new TypeError("Failed to fetch"), CHECKING],
    ["a gateway page", new ApiError(502, "<html>", {}, true), CHECKING],
    ["a 500 with no code", new ApiError(500, "could not save"), CHECKING],
    ["an unlisted code", new ApiError(409, "x", { code: "something_new" }), CHECKING],
  ];

  it.each(uncertain)("after %s settles the same id once, and says nothing about not being saved until it answers", async (_name, failure, text) => {
    const items = three();
    const answer = deferred<{ result: string }>();
    await readyToSave(items);
    mint.mockResolvedValueOnce({ attemptId: X1 });
    put.mockRejectedValueOnce(failure);
    settleCall.mockReturnValueOnce(answer.promise as never);
    pageCall.mockResolvedValue(pageOf());
    await pressSave();
    await waitFor(() => expect(settleCall).toHaveBeenCalledTimes(1));
    expect(screen.getByText(text)).toBeInTheDocument();
    expect(settleCall.mock.calls).toEqual([[X1, "52", "legacy_reason_save"]]);
    expect(document.body).not.toHaveTextContent(NOT_SAVED_WORDING);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(saveButton()).toBeDisabled();
    expect(radio(1, 3, "Keep text")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Check again" })).not.toBeInTheDocument();
    await act(async () => answer.resolve({ result: "applied" }));
    expect(await screen.findByText(SAVED)).toBeInTheDocument();
    expect(mint).toHaveBeenCalledTimes(1);
    expect(put).toHaveBeenCalledTimes(1);
    expect(settleCall).toHaveBeenCalledTimes(1);
    expect(document.body.innerHTML).not.toContain(X1);
    noDeletionWording();
  });

  describe("what settling answers", () => {
    // Every row starts from a PUT that did not say what happened.
    async function uncertainSave(items = three()) {
      await readyToSave(items);
      mint.mockResolvedValueOnce({ attemptId: X1 }).mockResolvedValueOnce({ attemptId: X2 });
      put.mockRejectedValueOnce(new TypeError("Failed to fetch"));
      return items;
    }

    it("applied: Saved, the page is read again with nothing carried, and the next press mints a fresh id", async () => {
      await uncertainSave();
      settleCall.mockResolvedValueOnce({ result: "applied" });
      const next = [reason("9", "52", { reviewHash: hash("9") })];
      pageCall.mockResolvedValueOnce(pageOf(...next));
      await pressSave();
      await screen.findByRole("group", { name: "Reason 1 of 1" });
      expect(screen.getByText(SAVED)).toBeInTheDocument();
      expect(checkedRadios()).toHaveLength(0);
      expect(screen.queryByText(/Changed since you looked/)).not.toBeInTheDocument();
      put.mockResolvedValueOnce({ kept: 1, removed: 0 });
      pageCall.mockResolvedValue(pageOf());
      await choose(1, 1, "Blank text");
      await pressSave();
      await waitFor(() => expect(put).toHaveBeenCalledTimes(2));
      expect(put.mock.calls[1]).toEqual(["52", X2, expectedDecisions(next, "remove")]);
    });

    it("not_applied: says so only now, reads the page again keeping the unchanged choices, and the next press mints a fresh id", async () => {
      const items = await uncertainSave();
      settleCall.mockResolvedValueOnce({ result: "not_applied" });
      pageCall.mockResolvedValueOnce(pageOf(...items));
      await pressSave();
      expect(await screen.findByText(NOT_APPLIED)).toBeInTheDocument();
      await screen.findByRole("group", { name: "Reason 1 of 3" });
      await waitFor(() => expect(checkedRadios()).toHaveLength(3));
      expect(screen.queryByText(/Changed since you looked/)).not.toBeInTheDocument();
      expect(saveButton()).toBeEnabled();
      expect(settleCall).toHaveBeenCalledTimes(1);
      put.mockResolvedValueOnce({ kept: 3, removed: 0 });
      pageCall.mockResolvedValue(pageOf());
      await pressSave();
      await waitFor(() => expect(put).toHaveBeenCalledTimes(2));
      expect(mint).toHaveBeenCalledTimes(2);
      expect(put.mock.calls[1]).toEqual(["52", X2, expectedDecisions(items)]);
    });

    it("history_unavailable: drops the id, holds Save and the list off until a reload succeeds, and keeps no choice", async () => {
      await uncertainSave();
      settleCall.mockResolvedValueOnce({ result: "history_unavailable" });
      await pressSave();
      expect(await screen.findByText(HISTORY)).toBeInTheDocument();
      expect(saveButton()).toBeDisabled();
      expect(radio(1, 3, "Keep text")).toBeDisabled();
      expect(screen.queryByRole("button", { name: "Check again" })).not.toBeInTheDocument();
      expect(pageCall).toHaveBeenCalledTimes(1);
      pageCall.mockResolvedValueOnce(pageOf(...three()));
      await userEvent.click(screen.getByRole("button", { name: "Reload" }));
      await screen.findByRole("group", { name: "Reason 1 of 3" });
      expect(screen.queryByText(HISTORY)).not.toBeInTheDocument();
      expect(checkedRadios()).toHaveLength(0);
      expect(saveButton()).toBeDisabled();
      await chooseAll(3, "Keep text");
      expect(saveButton()).toBeEnabled();
      expect(settleCall).toHaveBeenCalledTimes(1);
    });

    it("history_unavailable stays held when the reload fails", async () => {
      await uncertainSave();
      settleCall.mockResolvedValueOnce({ result: "history_unavailable" });
      await pressSave();
      await screen.findByText(HISTORY);
      pageCall.mockRejectedValueOnce(new ApiError(503, "x", { code: "reason_review_busy" }));
      await userEvent.click(screen.getByRole("button", { name: "Reload" }));
      expect(await screen.findByRole("alert")).toHaveTextContent(BUSY);
      expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
    });

    it.each([
      ["checking", () => settleCall.mockResolvedValueOnce({ result: "checking" })],
      ["a body with no result", () => settleCall.mockResolvedValueOnce({} as never)],
      ["an unknown result", () => settleCall.mockResolvedValueOnce({ result: "done" } as never)],
      ["a body that is not an object", () => settleCall.mockResolvedValueOnce(null as never)],
      ["a dropped connection", () => settleCall.mockRejectedValueOnce(new TypeError("Failed to fetch"))],
      ["a malformed answer", () => settleCall.mockRejectedValueOnce(new ApiError(502, "unexpected"))],
      ["a gateway page", () => settleCall.mockRejectedValueOnce(new ApiError(503, "<html>", {}, true))],
      ["a 500", () => settleCall.mockRejectedValueOnce(new ApiError(500, "oops"))],
      ["a 404", () => settleCall.mockRejectedValueOnce(new ApiError(404, "nope"))],
    ])("%s keeps the id open: Check again asks the same id, and nothing is minted or sent again", async (_name, arrange) => {
      const items = await uncertainSave();
      arrange();
      await pressSave();
      expect(await screen.findByText(STILL)).toBeInTheDocument();
      expect(document.body).not.toHaveTextContent(NOT_SAVED_WORDING);
      expect(saveButton()).toBeDisabled();
      expect(radio(1, 3, "Keep text")).toBeDisabled();
      settleCall.mockResolvedValueOnce({ result: "not_applied" });
      pageCall.mockResolvedValueOnce(pageOf(...items));
      await userEvent.click(screen.getByRole("button", { name: "Check again" }));
      expect(await screen.findByText(NOT_APPLIED)).toBeInTheDocument();
      expect(settleCall.mock.calls).toEqual([[X1, "52", "legacy_reason_save"], [X1, "52", "legacy_reason_save"]]);
      expect(mint).toHaveBeenCalledTimes(1);
      expect(put).toHaveBeenCalledTimes(1);
    });

    it("runs one settle for a double click of Check again, and the button is gone while it is in flight", async () => {
      await uncertainSave();
      const second = deferred<{ result: string }>();
      settleCall.mockResolvedValueOnce({ result: "checking" }).mockReturnValueOnce(second.promise as never);
      await pressSave();
      const again = await screen.findByRole("button", { name: "Check again" });
      act(() => {
        again.click();
        again.click();
      });
      await waitFor(() => expect(settleCall).toHaveBeenCalledTimes(2));
      expect(screen.queryByRole("button", { name: "Check again" })).not.toBeInTheDocument();
      expect(screen.getByText(CHECKING)).toBeInTheDocument();
      pageCall.mockResolvedValue(pageOf());
      await act(async () => second.resolve({ result: "applied" }));
      expect(await screen.findByText(SAVED)).toBeInTheDocument();
      expect(settleCall).toHaveBeenCalledTimes(2);
    });

    it.each([
      ["a 401", new ApiError(401, "x"), ERROR_COPY.signed_out],
      ["a coded 403", new ApiError(403, "x", { code: "reason_review_not_allowed" }), NOT_ALLOWED],
      ["a plain 403", new ApiError(403, "x"), ERROR_COPY.not_allowed],
    ])("%s on a settle ends the attempt and locks the page", async (_name, failure, copy) => {
      await uncertainSave();
      settleCall.mockRejectedValueOnce(failure);
      await pressSave();
      expect(await screen.findByRole("alert")).toHaveTextContent(copy);
      expect(screen.queryByRole("button")).not.toBeInTheDocument();
      expect(screen.queryByRole("radio")).not.toBeInTheDocument();
      noDeletionWording();
    });

    it("a restore on a settle holds the deletes, keeps the id open, and a later history answer ends it", async () => {
      await uncertainSave();
      settleCall.mockRejectedValueOnce(new ApiError(503, "x", { code: "restore_detected" })).mockResolvedValueOnce({ result: "history_unavailable" });
      await pressSave();
      expect(await screen.findByRole("alert")).toHaveTextContent(ERROR_COPY.restore_detected);
      expect(needsRestoreReload()).toBe(true);
      await userEvent.click(screen.getByRole("button", { name: "Check again" }));
      expect(await screen.findByText(HISTORY)).toBeInTheDocument();
      expect(settleCall.mock.calls.map((c) => c[0])).toEqual([X1, X1]);
    });
  });
});

describe("the reasons changed under the admin", () => {
  beforeEach(() => {
    walk.mockResolvedValue(unit(null, account("52", "1")));
  });

  it("a 409 reads the page again: an unchanged reason keeps its choice, a different or new one is marked and unchosen, a gone one is dropped, and focus lands on the first marked", async () => {
    const [a, b, c] = three();
    await readyToSave([a, b, c]);
    await choose(2, 3, "Blank text");
    const b2 = { ...b, reviewHash: hash("8") };
    const d = reason("4", "52", { reviewHash: hash("4") });
    mint.mockResolvedValueOnce({ attemptId: X1 }).mockResolvedValueOnce({ attemptId: X2 });
    put.mockRejectedValueOnce(new ApiError(409, "x", { code: "reason_review_changed" })).mockResolvedValueOnce({ kept: 2, removed: 1 });
    pageCall.mockResolvedValueOnce(pageOf(a, b2, d));
    await pressSave();
    await screen.findByText(CHANGED);
    await screen.findByRole("group", { name: "Reason 3 of 3" });
    expect(radio(1, 3, "Keep text")).toBeChecked();
    expect(checkedRadios()).toHaveLength(1);
    expect(screen.getAllByText("Changed since you looked: choose again")).toHaveLength(2);
    expect(document.body).not.toHaveTextContent("reason text 3");
    expect(document.body).toHaveTextContent("reason text 4");
    expect(settleCall).not.toHaveBeenCalled();
    await waitFor(() => expect(document.querySelector('[data-reason-heading="1"]')).toHaveFocus());
    expect(saveButton()).toBeDisabled();
    expect(document.body).not.toHaveTextContent(DELETION_WORDING);
    // The marked ones are chosen again, then a fresh id carries the new hashes in the new order.
    pageCall.mockResolvedValue(pageOf());
    await choose(2, 3, "Keep text");
    await choose(3, 3, "Blank text");
    await pressSave();
    await waitFor(() => expect(put).toHaveBeenCalledTimes(2));
    expect(mint).toHaveBeenCalledTimes(2);
    expect(put.mock.calls[1]).toEqual([
      "52",
      X2,
      [
        { requestId: "1", reviewHash: hash("1"), choice: "keep" },
        { requestId: "2", reviewHash: hash("8"), choice: "keep" },
        { requestId: "4", reviewHash: hash("4"), choice: "remove" },
      ],
    ]);
  });

  it("a 409 with nothing different moves focus to the message and keeps every choice", async () => {
    const items = three();
    await readyToSave(items);
    mint.mockResolvedValueOnce({ attemptId: X1 });
    put.mockRejectedValueOnce(new ApiError(409, "x", { code: "reason_review_changed" }));
    pageCall.mockResolvedValueOnce(pageOf(...items));
    await pressSave();
    await screen.findByRole("group", { name: "Reason 1 of 3" });
    await waitFor(() => expect(checkedRadios()).toHaveLength(3));
    expect(screen.queryByText(/Changed since you looked/)).not.toBeInTheDocument();
    expect(screen.getByRole("alert").parentElement).toHaveFocus();
    expect(saveButton()).toBeEnabled();
  });

  it("keeps the carried choices through a reload that fails and is tried again", async () => {
    const [a, b, c] = three();
    await readyToSave([a, b, c]);
    mint.mockResolvedValueOnce({ attemptId: X1 });
    put.mockRejectedValueOnce(new ApiError(409, "x", { code: "reason_review_changed" }));
    pageCall.mockRejectedValueOnce(new TypeError("Failed to fetch")).mockResolvedValueOnce(pageOf(a, { ...b, reviewHash: hash("8") }, c));
    await pressSave();
    await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
    await screen.findByRole("group", { name: "Reason 1 of 3" });
    await waitFor(() => expect(checkedRadios()).toHaveLength(2));
    expect(radio(1, 3, "Keep text")).toBeChecked();
    expect(radio(3, 3, "Keep text")).toBeChecked();
    expect(screen.getAllByText("Changed since you looked: choose again")).toHaveLength(1);
  });

  it("not_applied marks a reason whose hash changed, as a 409 does", async () => {
    const [a, b, c] = three();
    await readyToSave([a, b, c]);
    mint.mockResolvedValueOnce({ attemptId: X1 });
    put.mockRejectedValueOnce(new ApiError(502, "<html>", {}, true));
    settleCall.mockResolvedValueOnce({ result: "not_applied" });
    pageCall.mockResolvedValueOnce(pageOf(a, b, { ...c, reviewHash: hash("7") }));
    await pressSave();
    await screen.findByText(NOT_APPLIED);
    await screen.findByRole("group", { name: "Reason 3 of 3" });
    await waitFor(() => expect(checkedRadios()).toHaveLength(2));
    expect(screen.getAllByText("Changed since you looked: choose again")).toHaveLength(1);
    expect(document.querySelector('[data-reason-heading="2"]')).toHaveFocus();
    expect(saveButton()).toBeDisabled();
  });
});

describe("leaving, switching and late answers", () => {
  beforeEach(() => {
    walk.mockResolvedValue(unit(null, account("52", "1"), account("63", "1")));
  });

  it("does not carry a choice to another account that lists the same request", async () => {
    pageCall.mockResolvedValueOnce(pageOf(reason("1", "52"))).mockResolvedValueOnce(pageOf(reason("1", "63")));
    mountAccount("52");
    await screen.findByRole("group", { name: "Reason 1 of 1" });
    await choose(1, 1, "Blank text");
    expect(saveButton()).toBeEnabled();
    await userEvent.click(screen.getByRole("link", { name: "switch to 63" }));
    await waitFor(() => expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Deleted account · ref 63"));
    await screen.findByRole("group", { name: "Reason 1 of 1" });
    expect(checkedRadios()).toHaveLength(0);
    expect(saveButton()).toBeDisabled();
    expect(screen.queryByText(PRE_SAVE)).not.toBeInTheDocument();
    expect(pageCall.mock.calls).toEqual([["52", null], ["63", null]]);
  });

  it("ignores a page that answers for an account the admin has already left", async () => {
    const slow = deferred<ErasureReasonPage>();
    pageCall.mockReturnValueOnce(slow.promise).mockResolvedValueOnce(pageOf(reason("2", "63", { reason: "second account text" })));
    mountAccount("52");
    await userEvent.click(screen.getByRole("link", { name: "switch to 63" }));
    await screen.findByText("second account text");
    await act(async () => slow.resolve(pageOf(reason("1", "52", { reason: "first account text" }))));
    expect(document.body).not.toHaveTextContent("first account text");
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Deleted account · ref 63");
  });

  it("drops a save in flight when the admin goes back to the list, and ignores its late answer", async () => {
    const inflight = deferred<{ kept: number; removed: number }>();
    await readyToSave();
    mint.mockResolvedValueOnce({ attemptId: X1 });
    put.mockReturnValueOnce(inflight.promise);
    await pressSave();
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    await userEvent.click(screen.getByRole("link", { name: TITLE }));
    await screen.findByRole("link", { name: "Deleted account · ref 63" });
    await act(async () => inflight.resolve({ kept: 3, removed: 0 }));
    expect(pageCall).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(SAVED)).not.toBeInTheDocument();
    expect(settleCall).not.toHaveBeenCalled();
  });

  it("settles nothing for an uncertain answer that arrives after the page was left", async () => {
    const inflight = deferred<{ kept: number; removed: number }>();
    await readyToSave();
    mint.mockResolvedValueOnce({ attemptId: X1 });
    put.mockReturnValueOnce(inflight.promise);
    await pressSave();
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    await userEvent.click(screen.getByRole("link", { name: TITLE }));
    await act(async () => inflight.reject(new TypeError("Failed to fetch")));
    expect(settleCall).not.toHaveBeenCalled();
  });

  it("drops a settle in flight when the page is left; opening the account again reads a fresh page, which is empty if the save had applied", async () => {
    const answer = deferred<{ result: string }>();
    await readyToSave();
    mint.mockResolvedValueOnce({ attemptId: X1 });
    put.mockRejectedValueOnce(new TypeError("Failed to fetch"));
    settleCall.mockReturnValueOnce(answer.promise as never);
    await pressSave();
    await waitFor(() => expect(settleCall).toHaveBeenCalledTimes(1));
    await userEvent.click(screen.getByRole("link", { name: TITLE }));
    await act(async () => answer.resolve({ result: "applied" }));
    // The late answer changed nothing: no reload was asked for, and the entry is still listed.
    expect(pageCall).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("link", { name: "Deleted account · ref 52" })).toBeInTheDocument();
    expect(screen.queryByText(SAVED)).not.toBeInTheDocument();
    pageCall.mockResolvedValueOnce(pageOf());
    await userEvent.click(screen.getByRole("link", { name: "Deleted account · ref 52" }));
    expect(await screen.findByText(ACCOUNT_EMPTY)).toBeInTheDocument();
    expect(pageCall).toHaveBeenCalledTimes(2);
    expect(screen.queryByText(SAVED)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("link", { name: TITLE }));
    expect(labels().filter((l) => l?.startsWith("Deleted account"))).toEqual(["Deleted account · ref 63"]);
    expect(settleCall).toHaveBeenCalledTimes(1);
  });

  it("asks nothing and shows nothing when the page unmounts with a save in flight", async () => {
    const inflight = deferred<{ kept: number; removed: number }>();
    const view = await readyToSave();
    mint.mockResolvedValueOnce({ attemptId: X1 });
    put.mockReturnValueOnce(inflight.promise);
    await pressSave();
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    view.unmount();
    await act(async () => inflight.reject(new ApiError(500, "x", { code: "legacy_save_outcome_unknown" })));
    expect(settleCall).not.toHaveBeenCalled();
    expect(pageCall).toHaveBeenCalledTimes(1);
  });
});

describe("an account id too large for the mint", () => {
  it("lists and reads the full-width id as a string, refuses to save with no PUT, settle or request, and keeps the choices", async () => {
    const real = await vi.importActual<typeof import("../api/adminUsers")>("../api/adminUsers");
    const fetchSpy = vi.fn(() => Promise.reject(new Error("no request may be sent")));
    vi.stubGlobal("fetch", fetchSpy);
    try {
      const big = "9007199254740993";
      walk.mockResolvedValue(unit(null, account(big, "1"), account("9007199254740992", "1")));
      mint.mockImplementation(real.AdminUsers.mintAttempt);
      pageCall.mockResolvedValueOnce(pageOf(reason("1", big)));
      mountAccount(big);
      expect(await screen.findByRole("heading", { level: 1, name: `Deleted account · ref ${big}` })).toBeInTheDocument();
      await screen.findByRole("group", { name: "Reason 1 of 1" });
      expect(pageCall.mock.calls).toEqual([[big, null]]);
      await choose(1, 1, "Keep text");
      await pressSave();
      expect(await screen.findByRole("alert")).toHaveTextContent("That didn't work.");
      expect(mint).toHaveBeenCalledTimes(1);
      expect(mint.mock.calls[0]).toEqual([big, "legacy_reason_save"]);
      expect(put).not.toHaveBeenCalled();
      expect(settleCall).not.toHaveBeenCalled();
      expect(fetchSpy).not.toHaveBeenCalled();
      expect(radio(1, 1, "Keep text")).toBeChecked();
      expect(saveButton()).toBeEnabled();
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
