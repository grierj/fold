import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AdminUsers } from "../api/adminUsers";
import { ApiError } from "../api/core";
import { ERROR_COPY } from "../lib/errors";
import type { LegacyDiscoveryAccount, LegacyReasonDiscovery } from "../types";
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

  it("shows the account heading from the id and the search limit, without the plural intro or a request", async () => {
    await openFirst();
    expect(screen.getByRole("heading", { level: 1, name: "Deleted account · ref 9007199254740993" })).toBeInTheDocument();
    expect(screen.getByText(SEARCH_LIMIT)).toBeInTheDocument();
    expect(screen.queryByText(INTRO)).not.toBeInTheDocument();
    expect(AdminUsers.legacyReasonsPage).not.toHaveBeenCalled();
    expect(AdminUsers.mintAttempt).not.toHaveBeenCalled();
    expect(document.body).not.toHaveTextContent("Ann Smith");
  });

  it("goes back to the list with the walk intact and not started again", async () => {
    await openFirst();
    await userEvent.click(screen.getByRole("link", { name: TITLE }));
    expect(await screen.findByRole("link", { name: "Deleted account · ref 52" })).toBeInTheDocument();
    expect(screen.getByText(INTRO)).toBeInTheDocument();
    expect(walk).toHaveBeenCalledTimes(1);
  });

  it("keeps walking while an account is open, and shows the result on return", async () => {
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
      expect(AdminUsers.legacyReasonsPage).not.toHaveBeenCalled();
      expect(AdminUsers.mintAttempt).not.toHaveBeenCalled();
      expect(AdminUsers.settleAttempt).not.toHaveBeenCalled();
      expect(AdminUsers.saveLegacyReasons).not.toHaveBeenCalled();
    },
  );

  it("opens a deep link to a valid account directly, with the largest signed id intact", () => {
    walk.mockResolvedValue(unit(null));
    mount("/admin/deleted-reasons?account=9223372036854775807");
    const heading = screen.getByRole("heading", { level: 1 });
    expect(within(heading.parentElement as HTMLElement).getByText("Deleted account · ref 9223372036854775807")).toBe(heading);
  });
});
