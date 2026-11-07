import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AdminUsers, ApiError } from "../api";
import { reloadStarted, reloadSucceeded } from "../lib/deleteReload";
import { ERROR_COPY } from "../lib/errors";
import type { AttemptSettleResponse, ErasureReport, HandoffCandidate, SoleHostedEvent, SoleHostedEventsResponse, SoleLedGroup, User } from "../types";
import { DeleteUserPanel } from "./DeleteUserPanel";
import panelSource from "./DeleteUserPanel.tsx?raw";

vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 1, name: "Grier" } }) }));
vi.mock("../api", async (importActual) => ({
  ...(await importActual<typeof import("../api")>()),
  AdminUsers: { soleLeaderGroups: vi.fn(), soleHostEvents: vi.fn(), remove: vi.fn(), update: vi.fn(), mintAttempt: vi.fn(), settleAttempt: vi.fn() },
}));

const target: User = {
  id: 9, name: "Hana Host", displayName: "", email: "hana@example.com", phone: "", hideContactFromLeaders: true,
  avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", status: "active", hasPassword: true,
  accountType: "adult", timezone: "UTC",
};

const grier: HandoffCandidate = { id: 1, name: "Grier", admin: true };
const ana: HandoffCandidate = { id: 2, name: "Ana", admin: false };
const ben: HandoffCandidate = { id: 3, name: "Ben", admin: true };
const cy: HandoffCandidate = { id: 4, name: "Cy", admin: false };
const easter: SoleHostedEvent = { id: 10, title: "Easter breakfast", nextStartsAt: "2027-04-04T14:00:00Z" };
const picnic: SoleHostedEvent = { id: 11, title: "Church picnic" };

const hosted = (events: SoleHostedEvent[], candidates: HandoffCandidate[]): SoleHostedEventsResponse => ({ events, candidates });
const choir: SoleLedGroup = { id: 5, name: "Choir", status: "approved", candidates: [ana, grier] };

function open(action: "delete" | "disable" = "delete", onDone = vi.fn()) {
  render(<DeleteUserPanel target={target} action={action} onDone={onDone} />);
  return onDone;
}

// Like open, for the cases that close the panel: unmounting is closing.
function openClosable() {
  const onDone = vi.fn();
  const { unmount } = render(<DeleteUserPanel target={target} action="delete" onDone={onDone} />);
  return { onDone, close: unmount };
}

const eventSelect = (title: string) => screen.getByRole("combobox", { name: `New host for ${title}` });
const deleteButton = () => screen.getByRole("button", { name: "Delete account" });
const staleError = (data: Record<string, unknown>, code: string) => new ApiError(400, "stale", { code, ...data });

const noErasure: ErasureReport = { householdsKept: 0, householdsScrubbed: 0, peopleAnonymized: 0, guardianLinksRevoked: 0, activityEntriesKept: 0 };
let minted = 0;

beforeEach(() => {
  vi.resetAllMocks();
  minted = 0;
  reloadSucceeded(reloadStarted()); // no reload requirement carries over between tests
  vi.mocked(AdminUsers.soleLeaderGroups).mockResolvedValue({ groups: [] });
  vi.mocked(AdminUsers.soleHostEvents).mockResolvedValue(hosted([], []));
  vi.mocked(AdminUsers.mintAttempt).mockImplementation(async () => ({ attemptId: `attempt-${++minted}` }));
  vi.mocked(AdminUsers.remove).mockResolvedValue({ erasure: noErasure });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("DeleteUserPanel replacement hosts", () => {
  it("says it is checking while both lists load, and keeps Delete off", async () => {
    vi.mocked(AdminUsers.soleHostEvents).mockReturnValue(new Promise<SoleHostedEventsResponse>(() => undefined));
    open();
    expect(screen.getByRole("status")).toHaveTextContent("Checking their groups and events…");
    expect(deleteButton()).toBeDisabled();
    expect(AdminUsers.soleLeaderGroups).toHaveBeenCalledWith(9);
    expect(AdminUsers.soleHostEvents).toHaveBeenCalledWith(9);
  });

  it("shows a load failure and keeps Delete off", async () => {
    vi.mocked(AdminUsers.soleHostEvents).mockRejectedValue(new ApiError(500, "boom", { code: "server_error" }));
    open();
    expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't load their groups and events.");
    expect(deleteButton()).toBeDisabled();
  });

  it("deletes at once when they host nothing alone", async () => {
    const onDone = open();
    await waitFor(() => expect(deleteButton()).toBeEnabled());
    expect(screen.queryByText(/only host of these church-wide events/)).not.toBeInTheDocument();
    await userEvent.click(deleteButton());
    expect(await screen.findByText("The account was deleted.")).toBeInTheDocument();
    expect(AdminUsers.remove).toHaveBeenCalledWith(9, {}, {}, "attempt-1");
    expect(onDone).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(onDone).toHaveBeenCalledWith(true);
  });

  it("offers a labelled select per event, preselects nobody, and lists the admin as Me", async () => {
    vi.mocked(AdminUsers.soleHostEvents).mockResolvedValue(hosted([easter, picnic], [grier, ana, ben]));
    open();
    expect(await screen.findByText("They are the only host of these church-wide events. Choose who hosts each one next:")).toBeInTheDocument();
    for (const title of ["Easter breakfast", "Church picnic"]) {
      const select = eventSelect(title);
      expect(select).toHaveValue("");
      const options = within(select).getAllByRole("option").map((o) => o.textContent);
      expect(options).toEqual(["Choose a host", "Me (Grier)", "Ana", "Ben · admin"]);
      expect(within(select).getByRole("option", { name: "Choose a host" })).toBeDisabled();
    }
    expect(screen.getByText(/^Next date: /)).toBeInTheDocument();
    expect(screen.getByText("No upcoming dates")).toBeInTheDocument();
  });

  it("keeps the group picker's preselection of the deleting admin", async () => {
    vi.mocked(AdminUsers.soleLeaderGroups).mockResolvedValue({ groups: [choir] });
    open();
    expect(await screen.findByRole("combobox", { name: "Next leader of Choir" })).toHaveValue("1");
  });

  it("does not offer the shared choice for one event", async () => {
    vi.mocked(AdminUsers.soleHostEvents).mockResolvedValue(hosted([easter], [ana]));
    open();
    await screen.findByRole("combobox", { name: "New host for Easter breakfast" });
    expect(screen.queryByRole("combobox", { name: /Host for every event/ })).not.toBeInTheDocument();
  });

  it("fills every event from the shared choice and resets it when one changes", async () => {
    vi.mocked(AdminUsers.soleHostEvents).mockResolvedValue(hosted([easter, picnic], [grier, ana]));
    open();
    const every = await screen.findByRole("combobox", { name: /Host for every event/ });
    expect(every).toHaveValue("");
    expect(within(every).getAllByRole("option")[0]).toHaveTextContent("Choose for each event");
    await userEvent.selectOptions(every, "Ana");
    expect(eventSelect("Easter breakfast")).toHaveValue("2");
    expect(eventSelect("Church picnic")).toHaveValue("2");
    await userEvent.selectOptions(eventSelect("Church picnic"), "Me (Grier)");
    expect(every).toHaveValue("");
    expect(eventSelect("Easter breakfast")).toHaveValue("2");
    expect(eventSelect("Church picnic")).toHaveValue("1");
  });

  it("keeps Delete off, and counts what is left, until every event has a host", async () => {
    vi.mocked(AdminUsers.soleHostEvents).mockResolvedValue(hosted([easter, picnic], [ana]));
    open();
    const live = await screen.findByText("Choose a new host for 2 more events.");
    expect(live).toHaveAttribute("aria-live", "polite");
    expect(deleteButton()).toBeDisabled();
    await userEvent.selectOptions(eventSelect("Easter breakfast"), "Ana");
    expect(live).toHaveTextContent("Choose a new host for 1 more event.");
    expect(deleteButton()).toBeDisabled();
    await userEvent.selectOptions(eventSelect("Church picnic"), "Ana");
    expect(live).toBeEmptyDOMElement();
    expect(deleteButton()).toBeEnabled();
  });

  it("sends the picks keyed by event id", async () => {
    vi.mocked(AdminUsers.soleLeaderGroups).mockResolvedValue({ groups: [choir] });
    vi.mocked(AdminUsers.soleHostEvents).mockResolvedValue(hosted([easter, picnic], [grier, ana]));
    const onDone = open();
    await userEvent.selectOptions(await screen.findByRole("combobox", { name: "New host for Easter breakfast" }), "Ana");
    await userEvent.selectOptions(eventSelect("Church picnic"), "Me (Grier)");
    await userEvent.click(deleteButton());
    await userEvent.click(await screen.findByRole("button", { name: "Done" }));
    expect(AdminUsers.remove).toHaveBeenCalledWith(9, { "5": 1 }, { "10": 2, "11": 1 }, "attempt-1");
    expect(onDone).toHaveBeenCalledWith(true);
  });

  it("shows a calm message and keeps Delete off when no one else can host", async () => {
    vi.mocked(AdminUsers.soleHostEvents).mockResolvedValue(hosted([easter], []));
    open();
    const line = await screen.findByText("No one else can host these events yet. Add or enable an adult account, then come back.");
    expect(line.className).toContain("text-text-muted");
    expect(line).not.toHaveAttribute("role", "alert");
    expect(screen.getByText("Easter breakfast")).toBeInTheDocument();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(deleteButton()).toBeDisabled();
  });

  it("on a stale host list, updates events and candidates, keeps the picks that still fit, and focuses the first open event", async () => {
    const newer: SoleHostedEvent = { id: 12, title: "Newer event" };
    vi.mocked(AdminUsers.soleHostEvents).mockResolvedValue(hosted([easter, picnic], [ana, ben]));
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(staleError({ events: [easter, newer], candidates: [ana, cy] }, "needs_host_successor"));
    open();
    await userEvent.selectOptions(await screen.findByRole("combobox", { name: "New host for Easter breakfast" }), "Ana");
    await userEvent.selectOptions(eventSelect("Church picnic"), "Ben · admin");
    await userEvent.click(deleteButton());
    expect(await screen.findByRole("alert")).toHaveTextContent(ERROR_COPY.needs_host_successor);
    expect(screen.queryByRole("combobox", { name: "New host for Church picnic" })).not.toBeInTheDocument();
    expect(eventSelect("Easter breakfast")).toHaveValue("2");
    expect(eventSelect("Newer event")).toHaveValue("");
    expect(within(eventSelect("Newer event")).getAllByRole("option").map((o) => o.textContent)).toEqual(["Choose a host", "Ana", "Cy"]);
    await waitFor(() => expect(eventSelect("Newer event")).toHaveFocus());
    expect(deleteButton()).toBeDisabled();
    expect(screen.getByText("Choose a new host for 1 more event.")).toBeInTheDocument();
  });

  it("drops a pick whose person is no longer a candidate, and focuses the message when every event still has a host", async () => {
    vi.mocked(AdminUsers.soleHostEvents).mockResolvedValue(hosted([easter], [ana, ben]));
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(staleError({ events: [easter], candidates: [ana] }, "needs_host_successor"));
    open();
    await userEvent.selectOptions(await screen.findByRole("combobox", { name: "New host for Easter breakfast" }), "Ana");
    await userEvent.click(deleteButton());
    const alert = await screen.findByRole("alert");
    expect(eventSelect("Easter breakfast")).toHaveValue("2");
    await waitFor(() => expect(alert.parentElement).toHaveFocus());
    expect(deleteButton()).toBeEnabled();
  });

  it("ignores a stale body that has no lists", async () => {
    vi.mocked(AdminUsers.soleHostEvents).mockResolvedValue(hosted([easter], [ana]));
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(staleError({ events: "nope" }, "needs_host_successor"));
    open();
    await userEvent.selectOptions(await screen.findByRole("combobox", { name: "New host for Easter breakfast" }), "Ana");
    await userEvent.click(deleteButton());
    expect(await screen.findByRole("alert")).toHaveTextContent(ERROR_COPY.needs_host_successor);
    expect(eventSelect("Easter breakfast")).toHaveValue("2");
  });

  it("on a stale group list, updates the groups and preselects the admin for a new one", async () => {
    const youth: SoleLedGroup = { id: 6, name: "Youth", status: "approved", candidates: [ben, grier] };
    vi.mocked(AdminUsers.soleLeaderGroups).mockResolvedValue({ groups: [choir] });
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(staleError({ groups: [{ ...choir, candidates: [ana, ben] }, youth] }, "needs_successor"));
    open();
    expect(await screen.findByRole("combobox", { name: "Next leader of Choir" })).toHaveValue("1");
    await userEvent.click(deleteButton());
    expect(await screen.findByRole("alert")).toHaveTextContent(ERROR_COPY.needs_successor);
    expect(screen.getByRole("combobox", { name: "Next leader of Choir" })).toHaveValue("2");
    expect(screen.getByRole("combobox", { name: "Next leader of Youth" })).toHaveValue("1");
  });
});

describe("DeleteUserPanel disable", () => {
  beforeEach(() => {
    vi.mocked(AdminUsers.update).mockResolvedValue({ user: target });
  });

  it("makes no host request, shows no host section, and sends only group picks", async () => {
    vi.mocked(AdminUsers.soleLeaderGroups).mockResolvedValue({ groups: [choir] });
    const onDone = open("disable");
    expect(screen.getByRole("status")).toHaveTextContent("Checking their groups…");
    await screen.findByRole("combobox", { name: "Next leader of Choir" });
    expect(AdminUsers.soleHostEvents).not.toHaveBeenCalled();
    expect(screen.queryByText(/church-wide events/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Disable account" }));
    expect(AdminUsers.update).toHaveBeenCalledWith(9, { status: "disabled", successors: { "5": 1 } });
    expect(AdminUsers.remove).not.toHaveBeenCalled();
    expect(AdminUsers.mintAttempt).not.toHaveBeenCalled();
    await waitFor(() => expect(onDone).toHaveBeenCalledWith(true));
  });
});

// ---- T07B-1c: outcomes of a press of Delete ----

const NOTHING = "Nothing was deleted by this attempt.";
const ALREADY = "The account is already deleted; this attempt did not delete it.";
const HISTORY = "Fold can't tell whether an earlier delete finished. Reload this person's page to check.";
const CHECKING = "Checking what happened to this delete request…";
const NOT_DELETED = /nothing was deleted|not deleted|wasn't deleted|was not deleted/i;

const coded = (status: number, code: string, data: Record<string, unknown> = {}) => new ApiError(status, "server text", { code, ...data });
const settleResult = (result: AttemptSettleResponse["result"], subjectDeleted?: boolean): AttemptSettleResponse =>
  subjectDeleted === undefined ? { result } : { result, subjectDeleted };

// A settle call the test answers by hand, so the copy before the answer can be read.
function heldSettle() {
  let answer!: (r: AttemptSettleResponse) => void;
  let fail!: (e: unknown) => void;
  vi.mocked(AdminUsers.settleAttempt).mockReturnValueOnce(new Promise<AttemptSettleResponse>((resolve, reject) => {
    answer = resolve;
    fail = reject;
  }));
  return { answer: (r: AttemptSettleResponse) => answer(r), fail: (e: unknown) => fail(e) };
}

async function press() {
  await waitFor(() => expect(deleteButton()).toBeEnabled());
  await userEvent.click(deleteButton());
}

const removedIds = () => vi.mocked(AdminUsers.remove).mock.calls.map((call) => call[3]);

describe("DeleteUserPanel attempt ids", () => {
  it("mints a fresh id for every press and sends each one with the DELETE", async () => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(409, "delete_household_busy"));
    open();
    await press();
    expect(await screen.findByRole("alert")).toHaveTextContent(ERROR_COPY.delete_household_busy);
    await press();
    await screen.findByText("The account was deleted.");
    expect(AdminUsers.mintAttempt).toHaveBeenCalledTimes(2);
    expect(AdminUsers.mintAttempt).toHaveBeenCalledWith(9);
    expect(removedIds()).toEqual(["attempt-1", "attempt-2"]);
    expect(AdminUsers.settleAttempt).not.toHaveBeenCalled();
  });

  it("never puts the id on the page", async () => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(500, "delete_outcome_unknown"));
    vi.mocked(AdminUsers.settleAttempt).mockResolvedValue(settleResult("not_applied"));
    open();
    await press();
    await screen.findByText(NOTHING);
    expect(document.body.innerHTML).not.toContain("attempt-1");
  });

  it("sends no id for a disable", async () => {
    vi.mocked(AdminUsers.update).mockResolvedValue({ user: target });
    open("disable");
    await userEvent.click(await screen.findByRole("button", { name: "Disable account" }));
    await waitFor(() => expect(AdminUsers.update).toHaveBeenCalled());
    expect(AdminUsers.mintAttempt).not.toHaveBeenCalled();
    expect(AdminUsers.settleAttempt).not.toHaveBeenCalled();
  });

  it("says nothing happened and stays enabled when the mint fails, since no DELETE was sent", async () => {
    vi.mocked(AdminUsers.mintAttempt).mockRejectedValueOnce(new TypeError("Failed to fetch"));
    open();
    await press();
    expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't reach Fold");
    expect(AdminUsers.remove).not.toHaveBeenCalled();
    expect(deleteButton()).toBeEnabled();
    await press();
    await screen.findByText("The account was deleted.");
    // The failed mint sent nothing; the next press mints the first id the mock hands out.
    expect(removedIds()).toEqual(["attempt-1"]);
  });

  it("locks Delete when the mint says a restore was seen", async () => {
    vi.mocked(AdminUsers.mintAttempt).mockRejectedValueOnce(coded(503, "restore_detected"));
    open();
    await press();
    expect(await screen.findByRole("alert")).toHaveTextContent(ERROR_COPY.restore_detected);
    expect(AdminUsers.remove).not.toHaveBeenCalled();
    expect(deleteButton()).toBeDisabled();
  });
});

describe("DeleteUserPanel uncertain answers settle the same id", () => {
  const uncertain: [string, () => unknown, string][] = [
    ["delete_outcome_unknown", () => coded(500, "delete_outcome_unknown"), CHECKING],
    ["attempt_closed", () => coded(409, "attempt_closed"), ERROR_COPY.attempt_closed],
    ["attempt_expired", () => coded(409, "attempt_expired"), ERROR_COPY.attempt_expired],
    ["a rejected fetch", () => new TypeError("Failed to fetch"), CHECKING],
    ["an aborted fetch", () => new DOMException("aborted", "AbortError"), CHECKING],
    ["a non-JSON 5xx", () => new ApiError(502, "request failed (502)", {}), CHECKING],
    ["a status no table row names", () => coded(418, "teapot"), CHECKING],
  ];

  it.each(uncertain)("%s checks, with no not-deleted copy before the answer", async (_name, make, lead) => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(make());
    const settle = heldSettle();
    open();
    await press();
    expect(await screen.findByText(lead)).toBeInTheDocument();
    expect(AdminUsers.settleAttempt).toHaveBeenCalledWith("attempt-1", 9);
    expect(document.body).not.toHaveTextContent(NOT_DELETED);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(deleteButton()).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Check again" })).not.toBeInTheDocument();
    settle.answer(settleResult("not_applied"));
    expect(await screen.findByText(NOTHING)).toBeInTheDocument();
    expect(deleteButton()).toBeEnabled();
    expect(AdminUsers.mintAttempt).toHaveBeenCalledTimes(1);
  });

  it("says an expired request was too old to run before it settles, then each settle answer follows", async () => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(409, "attempt_expired"));
    const settle = heldSettle();
    open();
    await press();
    expect(await screen.findByRole("status")).toHaveTextContent("This delete request was too old to run, so Fold didn't run it.");
    settle.answer(settleResult("history_unavailable"));
    expect(await screen.findByText(HISTORY)).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(NOT_DELETED);
    expect(deleteButton()).toBeDisabled();
  });

  it.each<[string, AttemptSettleResponse, string]>([
    ["applied", settleResult("applied"), "The account was deleted."],
    ["not_applied", settleResult("not_applied"), NOTHING],
    ["not_applied with subjectDeleted", settleResult("not_applied", true), ALREADY],
    ["history_unavailable", settleResult("history_unavailable"), HISTORY],
  ])("an expired id then settle %s", async (_name, answer, copy) => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(409, "attempt_expired"));
    vi.mocked(AdminUsers.settleAttempt).mockResolvedValueOnce(answer);
    open();
    await press();
    expect(await screen.findByText(copy)).toBeInTheDocument();
  });
});

describe("DeleteUserPanel settle answers", () => {
  async function pressUnknown(answer: AttemptSettleResponse, onDone = vi.fn()) {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(500, "delete_outcome_unknown"));
    vi.mocked(AdminUsers.settleAttempt).mockResolvedValueOnce(answer);
    open("delete", onDone);
    await press();
    return onDone;
  }

  it("applied: the account was deleted, with no counts it was never told", async () => {
    const onDone = await pressUnknown(settleResult("applied"));
    expect(await screen.findByText("The account was deleted.")).toBeInTheDocument();
    expect(screen.queryByText("Households kept")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete account" })).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("status")).toHaveFocus());
    expect(onDone).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(onDone).toHaveBeenCalledWith(true);
  });

  it("not_applied: says nothing was deleted by this attempt, and the next press mints a fresh id", async () => {
    await pressUnknown(settleResult("not_applied"));
    expect(await screen.findByText(NOTHING)).toBeInTheDocument();
    expect(deleteButton()).toBeEnabled();
    await press();
    await screen.findByText("The account was deleted.");
    expect(removedIds()).toEqual(["attempt-1", "attempt-2"]);
    expect(AdminUsers.settleAttempt).toHaveBeenCalledTimes(1);
  });

  it("not_applied with subjectDeleted: says the account is already deleted and offers no delete", async () => {
    const onDone = await pressUnknown(settleResult("not_applied", true));
    expect(await screen.findByText(ALREADY)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete account" })).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("status")).toHaveFocus());
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onDone).toHaveBeenCalledWith(true);
  });

  it("history_unavailable: says Fold can't tell, keeps Delete off, and never says nothing was deleted", async () => {
    const onDone = await pressUnknown(settleResult("history_unavailable"));
    expect(await screen.findByText(HISTORY)).toBeInTheDocument();
    expect(deleteButton()).toBeDisabled();
    expect(document.body).not.toHaveTextContent(NOT_DELETED);
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onDone).toHaveBeenCalledWith(true);
  });

  it("checking: says it is still checking and checks the same id again", async () => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(500, "delete_outcome_unknown"));
    vi.mocked(AdminUsers.settleAttempt).mockResolvedValueOnce(settleResult("checking")).mockResolvedValueOnce(settleResult("applied"));
    open();
    await press();
    expect(await screen.findByText(/^Still checking\./)).toBeInTheDocument();
    expect(deleteButton()).toBeDisabled();
    expect(document.body).not.toHaveTextContent(NOT_DELETED);
    await waitFor(() => expect(screen.getByRole("status")).toHaveFocus());
    await userEvent.click(screen.getByRole("button", { name: "Check again" }));
    expect(await screen.findByText("The account was deleted.")).toBeInTheDocument();
    expect(AdminUsers.settleAttempt).toHaveBeenNthCalledWith(1, "attempt-1", 9);
    expect(AdminUsers.settleAttempt).toHaveBeenNthCalledWith(2, "attempt-1", 9);
    expect(AdminUsers.mintAttempt).toHaveBeenCalledTimes(1);
  });

  it.each([
    ["a network failure", () => new TypeError("Failed to fetch")],
    ["a 5xx", () => new ApiError(502, "bad gateway", {})],
  ])("%s on the settle call keeps checking with Check again", async (_name, make) => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(500, "delete_outcome_unknown"));
    vi.mocked(AdminUsers.settleAttempt).mockRejectedValueOnce(make()).mockResolvedValueOnce(settleResult("not_applied"));
    open();
    await press();
    expect(await screen.findByText(/^Still checking\./)).toBeInTheDocument();
    expect(deleteButton()).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Check again" }));
    expect(await screen.findByText(NOTHING)).toBeInTheDocument();
    expect(AdminUsers.settleAttempt).toHaveBeenNthCalledWith(2, "attempt-1", 9);
  });

  it("an unrecognised settle result is treated as still checking", async () => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(500, "delete_outcome_unknown"));
    vi.mocked(AdminUsers.settleAttempt).mockResolvedValueOnce({ result: "mystery" } as unknown as AttemptSettleResponse);
    open();
    await press();
    expect(await screen.findByText(/^Still checking\./)).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(NOT_DELETED);
  });

  it.each([
    ["401", coded(401, "signed_out"), ERROR_COPY.signed_out],
    ["403", coded(403, "not_allowed"), ERROR_COPY.not_allowed],
  ])("a settle %s ends the attempt with no Check again", async (_name, error, copy) => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(500, "delete_outcome_unknown"));
    vi.mocked(AdminUsers.settleAttempt).mockRejectedValueOnce(error);
    open();
    await press();
    expect(await screen.findByRole("alert")).toHaveTextContent(copy);
    expect(deleteButton()).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Check again" })).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(NOT_DELETED);
  });

  it("a settle restore_detected says so and lets the person check again; the restart answers history_unavailable", async () => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(500, "delete_outcome_unknown"));
    vi.mocked(AdminUsers.settleAttempt)
      .mockRejectedValueOnce(coded(503, "restore_detected"))
      .mockResolvedValueOnce(settleResult("history_unavailable"));
    open();
    await press();
    expect(await screen.findByText(ERROR_COPY.restore_detected)).toBeInTheDocument();
    expect(deleteButton()).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Check again" }));
    expect(await screen.findByText(HISTORY)).toBeInTheDocument();
    expect(AdminUsers.settleAttempt).toHaveBeenNthCalledWith(2, "attempt-1", 9);
  });

  it("two presses by one admin: the second settle says the account is already deleted, naming no one", async () => {
    vi.mocked(AdminUsers.remove)
      .mockRejectedValueOnce(coded(500, "delete_outcome_unknown"))
      .mockRejectedValueOnce(new TypeError("Failed to fetch"));
    vi.mocked(AdminUsers.settleAttempt)
      .mockResolvedValueOnce(settleResult("not_applied"))
      .mockResolvedValueOnce(settleResult("not_applied", true));
    open();
    await press();
    expect(await screen.findByText(NOTHING)).toBeInTheDocument();
    await press();
    const line = await screen.findByText(ALREADY);
    expect(removedIds()).toEqual(["attempt-1", "attempt-2"]);
    expect(AdminUsers.settleAttempt).toHaveBeenNthCalledWith(1, "attempt-1", 9);
    expect(AdminUsers.settleAttempt).toHaveBeenNthCalledWith(2, "attempt-2", 9);
    // The status box says only this: no other person, admin, or "someone".
    const status = line.closest("[role=status]");
    expect(status?.textContent).toBe(ALREADY);
    expect(status?.textContent).not.toMatch(/someone|another|other|admin|by you|you /i);
    expect(screen.queryByText(NOTHING)).not.toBeInTheDocument();
  });
});

describe("DeleteUserPanel definitive answers", () => {
  // Rows of T07B-D4 R4-C08 that end the id before the server answers: the id
  // is dropped, settle is never called, and a press where the table enables
  // the action sends a new id.
  const rows: [string, () => ApiError, string, boolean][] = [
    ["delete_timed_out", () => coded(503, "delete_timed_out"), ERROR_COPY.delete_timed_out, true],
    ["delete_household_busy", () => coded(409, "delete_household_busy"), ERROR_COPY.delete_household_busy, true],
    ["admin_account_busy", () => coded(409, "admin_account_busy"), ERROR_COPY.admin_account_busy, true],
    ["attempt_busy", () => coded(503, "attempt_busy"), ERROR_COPY.attempt_busy, true],
    ["last admin", () => coded(400, "last_admin"), ERROR_COPY.last_admin, true],
    ["your own account", () => coded(400, "not_yourself"), ERROR_COPY.not_yourself, true],
    ["successors", () => coded(400, "needs_successor"), ERROR_COPY.needs_successor, true],
    ["hosts", () => coded(400, "needs_host_successor"), ERROR_COPY.needs_host_successor, true],
    ["an invalid body", () => new ApiError(400, "invalid host successors", {}), "That didn't work. Please try again.", true],
    ["401", () => coded(401, "signed_out"), ERROR_COPY.signed_out, false],
    ["403", () => coded(403, "not_allowed"), ERROR_COPY.not_allowed, false],
    ["404", () => coded(404, "not_found"), "This account no longer exists.", false],
    ["restore_detected", () => coded(503, "restore_detected"), ERROR_COPY.restore_detected, false],
  ];

  it.each(rows)("%s shows its copy and discards the id", async (_name, make, copy, enabled) => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(make());
    open();
    await press();
    expect(await screen.findByRole("alert")).toHaveTextContent(copy);
    expect(AdminUsers.settleAttempt).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "Check again" })).not.toBeInTheDocument();
    if (enabled) {
      expect(deleteButton()).toBeEnabled();
      await press();
      await screen.findByText("The account was deleted.");
      expect(removedIds()).toEqual(["attempt-1", "attempt-2"]);
    } else {
      expect(deleteButton()).toBeDisabled();
      expect(AdminUsers.mintAttempt).toHaveBeenCalledTimes(1);
      expect(screen.queryByText(NOTHING)).not.toBeInTheDocument();
    }
  });

  it("a 404 closes with a reload of the list; a 403 closes without one", async () => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(404, "not_found"));
    const onDone = open();
    await press();
    await screen.findByText("This account no longer exists.");
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onDone).toHaveBeenCalledWith(true);
    cleanup();
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(403, "not_allowed"));
    const second = open();
    await press();
    await screen.findByText(ERROR_COPY.not_allowed);
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(second).toHaveBeenCalledWith(false);
  });

  it("keeps the person's picks and fresh-mints after a stale host list", async () => {
    vi.mocked(AdminUsers.soleHostEvents).mockResolvedValue(hosted([easter], [ana]));
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(staleError({ events: [easter], candidates: [ana] }, "needs_host_successor"));
    open();
    await userEvent.selectOptions(await screen.findByRole("combobox", { name: "New host for Easter breakfast" }), "Ana");
    await userEvent.click(deleteButton());
    await screen.findByRole("alert");
    await press();
    await screen.findByText("The account was deleted.");
    expect(removedIds()).toEqual(["attempt-1", "attempt-2"]);
  });

  it("keeps no retry control enabled while an outcome is unresolved", async () => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(500, "delete_outcome_unknown"));
    const settle = heldSettle();
    open();
    await press();
    await screen.findByText(CHECKING);
    expect(deleteButton()).toBeDisabled();
    settle.answer(settleResult("checking"));
    await screen.findByText(/^Still checking\./);
    expect(deleteButton()).toBeDisabled();
    for (const button of screen.getAllByRole("button")) {
      if (button.textContent !== "Cancel" && button.textContent !== "Check again") expect(button).toBeDisabled();
    }
  });
});

describe("DeleteUserPanel done", () => {
  it("shows the counts as numbers, makes no new claim, and closes only on Done", async () => {
    vi.mocked(AdminUsers.remove).mockResolvedValue({ erasure: { ...noErasure, householdsKept: 2, activityEntriesKept: 5 } });
    const onDone = open();
    await press();
    expect(await screen.findByText("The account was deleted.")).toBeInTheDocument();
    const counts = screen.getByRole("status").querySelectorAll("dt, dd");
    expect([...counts].map((el) => el.textContent)).toEqual(["Households kept", "2", "Activity entries kept", "5"]);
    expect(screen.queryByText(/Delete .*\?/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel" })).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("status")).toHaveFocus());
    expect(onDone).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(onDone).toHaveBeenCalledWith(true);
  });
});

// R4-6c: what a delete does not reach. Landed code still retains network
// addresses in the activity log (RETAIN_* rows and conversion groups, inventory
// IR-1 to IR-4, gate G-9), so the IP sentence is shown before the press and after
// it, whatever the counts say.
describe("DeleteUserPanel what a delete does not reach", () => {
  const IP = "Some network (IP) addresses can remain in the activity log, and Fold can't always tell whose address an entry holds.";
  const BACKUPS = "Deleting reaches Fold's backups only as they expire, so their details can stay in an older backup until then.";
  const OUTSIDE = "Copies already sent to mailboxes, mail providers and push services are outside Fold, and Fold can't take them back.";

  it("says before the press that backups, outside copies and some network addresses remain, while Delete is on", async () => {
    open();
    expect(await screen.findByText(BACKUPS, { exact: false })).toBeInTheDocument();
    expect(screen.getByText(OUTSIDE, { exact: false })).toBeInTheDocument();
    expect(screen.getByText(IP, { exact: false })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete account" })).toBeEnabled();
    expect(screen.queryByText(/Households kept/)).not.toBeInTheDocument();
  });

  it("is plain text in the panel, not a status or an alert that interrupts", async () => {
    open();
    await waitFor(() => expect(deleteButton()).toBeEnabled());
    expect(screen.getByText(IP, { exact: false })).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps the IP sentence after the delete, including when the report counts nothing kept", async () => {
    for (const activityEntriesKept of [0, 3]) {
      vi.mocked(AdminUsers.remove).mockResolvedValue({ erasure: { ...noErasure, activityEntriesKept } });
      const view = render(<DeleteUserPanel target={target} action="delete" onDone={vi.fn()} />);
      await press();
      await screen.findByText("The account was deleted.");
      expect(screen.getByText(IP, { exact: false })).toBeInTheDocument();
      expect(screen.getByText(BACKUPS, { exact: false })).toBeInTheDocument();
      expect(screen.getByText(OUTSIDE, { exact: false })).toBeInTheDocument();
      view.unmount();
    }
  });

  it("keeps the sentences when the outcome is uncertain, and for a settled delete with no report", async () => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(500, "delete_outcome_unknown"));
    vi.mocked(AdminUsers.settleAttempt).mockResolvedValueOnce(settleResult("applied"));
    open();
    await press();
    await screen.findByText("The account was deleted.");
    expect(screen.getByText(IP, { exact: false })).toBeInTheDocument();
    expect(screen.queryByText(/Households kept/)).not.toBeInTheDocument();
  });

  it("explains the two counts without claiming more than the server says", async () => {
    vi.mocked(AdminUsers.remove).mockResolvedValue({ erasure: { ...noErasure, householdsKept: 2, activityEntriesKept: 5 } });
    open();
    await press();
    await screen.findByText("The account was deleted.");
    const households = screen.getByText(/Fold left these households as they were/);
    expect(households).toHaveTextContent("other people still share them");
    const activity = screen.getByText(/activity-log entries linked to this person/);
    expect(activity).toHaveTextContent("Fold counts entries, not addresses");
    expect(activity).toHaveTextContent("a 0 doesn't promise none remain");
    for (const el of [households, activity]) expect(el.textContent).not.toMatch(/all (gone|removed|erased)|completely|nothing (is |remains|was)/i);
  });

  it("does not show the delete notes for a disable", async () => {
    open("disable");
    await screen.findByRole("button", { name: "Disable account" });
    expect(screen.queryByText(/backups/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/network \(IP\)/)).not.toBeInTheDocument();
  });
});

describe("DeleteUserPanel after the panel closes", () => {
  function deferred<T>() {
    let resolve!: (v: T) => void;
    let reject!: (e: unknown) => void;
    const promise = new Promise<T>((res, rej) => {
      resolve = res;
      reject = rej;
    });
    return { promise, resolve, reject };
  }

  it("ignores a DELETE that finishes after close", async () => {
    const del = deferred<{ erasure: ErasureReport }>();
    vi.mocked(AdminUsers.remove).mockReturnValueOnce(del.promise);
    const { onDone, close } = openClosable();
    await press();
    await waitFor(() => expect(AdminUsers.remove).toHaveBeenCalled());
    close();
    del.resolve({ erasure: noErasure });
    await del.promise;
    expect(onDone).not.toHaveBeenCalled();
  });

  it("does not settle an id whose DELETE answered unknown after close", async () => {
    const del = deferred<{ erasure: ErasureReport }>();
    vi.mocked(AdminUsers.remove).mockReturnValueOnce(del.promise);
    const { onDone, close } = openClosable();
    await press();
    await waitFor(() => expect(AdminUsers.remove).toHaveBeenCalled());
    close();
    del.reject(coded(500, "delete_outcome_unknown"));
    await del.promise.catch(() => undefined);
    await Promise.resolve();
    expect(AdminUsers.settleAttempt).not.toHaveBeenCalled();
    expect(onDone).not.toHaveBeenCalled();
  });

  it("sends no DELETE when the mint finishes after close", async () => {
    const mint = deferred<{ attemptId: string }>();
    vi.mocked(AdminUsers.mintAttempt).mockReturnValueOnce(mint.promise);
    const { close } = openClosable();
    await press();
    await waitFor(() => expect(AdminUsers.mintAttempt).toHaveBeenCalled());
    close();
    mint.resolve({ attemptId: "attempt-late" });
    await mint.promise;
    await Promise.resolve();
    expect(AdminUsers.remove).not.toHaveBeenCalled();
  });

  it("ignores a settle answer that arrives after close", async () => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(500, "delete_outcome_unknown"));
    const settle = heldSettle();
    const { onDone, close } = openClosable();
    await press();
    await screen.findByText(CHECKING);
    close();
    settle.answer(settleResult("applied"));
    await Promise.resolve();
    expect(onDone).not.toHaveBeenCalled();
    expect(screen.queryByText("The account was deleted.")).not.toBeInTheDocument();
  });

  it("a reopened panel starts with a fresh id and does not reuse the dropped one", async () => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(500, "delete_outcome_unknown"));
    vi.mocked(AdminUsers.settleAttempt).mockReturnValueOnce(new Promise<AttemptSettleResponse>(() => undefined));
    const first = openClosable();
    await press();
    await screen.findByText(CHECKING);
    first.close();
    open();
    await press();
    await screen.findByText("The account was deleted.");
    expect(removedIds()).toEqual(["attempt-1", "attempt-2"]);
    expect(AdminUsers.settleAttempt).toHaveBeenCalledTimes(1);
  });
});

describe("DeleteUserPanel wording", () => {
  it("the panel source carries no reason-review wording in this release", () => {
    expect(panelSource).not.toMatch(/reason[\s_-]*review|needs_reason|reviewHash/i);
  });
});

// ---- review fixes: the real fetch wrapper, a changed action or person, and the reload requirement ----

describe("DeleteUserPanel through the real fetch wrapper", () => {
  const ID = "0123456789abcdef".repeat(5);
  type Call = { method: string; path: string; body: Record<string, unknown> | undefined };

  // Routes the panel's three attempt calls to the real AdminUsers functions and
  // answers the network by hand.
  async function wire(answer: (call: Call) => Response) {
    const real = await vi.importActual<typeof import("../api")>("../api");
    vi.mocked(AdminUsers.mintAttempt).mockImplementation(real.AdminUsers.mintAttempt);
    vi.mocked(AdminUsers.remove).mockImplementation(real.AdminUsers.remove);
    vi.mocked(AdminUsers.settleAttempt).mockImplementation(real.AdminUsers.settleAttempt);
    const calls: Call[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
      const path = String(url).replace("/api/v1", "");
      if (path === "/auth/csrf") return new Response(JSON.stringify({ csrfToken: "csrf" }), { status: 200 });
      const call: Call = { method: init?.method ?? "GET", path, body: init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : undefined };
      calls.push(call);
      return answer(call);
    }));
    return calls;
  }
  const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
  const html = (status = 200) => new Response("<html>gateway</html>", { status, headers: { "Content-Type": "text/html" } });
  const MINT = "/admin/operation-attempts";
  const SETTLE = "/admin/operation-attempts/settle";

  it("sends no DELETE when the mint answers 200 with a page that is not JSON", async () => {
    const calls = await wire((c) => (c.path === MINT ? html() : json({})));
    open();
    await press();
    expect(await screen.findByRole("alert")).toHaveTextContent(/didn't work/);
    expect(calls.filter((c) => c.method === "DELETE")).toEqual([]);
    expect(document.body).not.toHaveTextContent("The account was deleted.");
    expect(deleteButton()).toBeEnabled();
  });

  it.each([
    ["JSON with no id", {}],
    ["a short id", { attemptId: "abc" }],
  ])("sends no DELETE when the mint answers %s", async (_name, body) => {
    const calls = await wire((c) => (c.path === MINT ? json(body) : json({})));
    open();
    await press();
    await screen.findByRole("alert");
    expect(calls.filter((c) => c.method === "DELETE")).toEqual([]);
  });

  it.each([
    ["an HTML page", () => html()],
    ["an empty body", () => new Response("", { status: 200 })],
    ["JSON that is not a report", () => json({})],
  ])("does not say deleted when the DELETE answers 200 with %s; it settles the same id", async (_name, deleteAnswer) => {
    const calls = await wire((c) => {
      if (c.path === MINT) return json({ attemptId: ID });
      if (c.method === "DELETE") return deleteAnswer();
      return json({ result: "not_applied" });
    });
    open();
    await press();
    expect(await screen.findByText(NOTHING)).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("The account was deleted.");
    const sent = calls.find((c) => c.method === "DELETE")?.body?.attemptId;
    expect(sent).toBe(ID);
    const settle = calls.filter((c) => c.path === SETTLE);
    expect(settle).toHaveLength(1);
    expect(settle[0].body).toEqual({ attemptId: ID, kind: "account_delete", subjectUserId: 9 });
    expect(calls.filter((c) => c.path === MINT)).toHaveLength(1);
  });

  it.each([
    ["an HTML page", () => html(400)],
    ["an empty body", () => new Response("", { status: 400 })],
    ["an HTML page with a 5xx status", () => html(502)],
  ])("a DELETE answered %s settles the retained id, with Delete off until the answer", async (_name, deleteAnswer) => {
    let answerSettle!: (r: Response) => void;
    const held = new Promise<Response>((resolve) => {
      answerSettle = resolve;
    });
    const calls = await wire((c) => {
      if (c.path === MINT) return json({ attemptId: ID });
      if (c.method === "DELETE") return deleteAnswer();
      return json({});
    });
    // Hold the settle by hand: the stub above answers synchronously.
    const base = vi.mocked(globalThis.fetch).getMockImplementation();
    vi.stubGlobal("fetch", vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => (String(url).endsWith(SETTLE) ? held : base?.(url, init))));
    open();
    await press();
    expect(await screen.findByText(CHECKING)).toBeInTheDocument();
    expect(deleteButton()).toBeDisabled();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(NOT_DELETED);
    answerSettle(json({ result: "not_applied" }));
    expect(await screen.findByText(NOTHING)).toBeInTheDocument();
    expect(calls.find((c) => c.method === "DELETE")?.body?.attemptId).toBe(ID);
    expect(AdminUsers.settleAttempt).toHaveBeenCalledWith(ID, 9);
    expect(AdminUsers.mintAttempt).toHaveBeenCalledTimes(1);
  });

  it("a genuine 400 refusal, JSON from the server, stays definitive: its copy, no settle, a fresh id next", async () => {
    const calls = await wire((c) =>
      c.path === MINT ? json({ attemptId: ID }) : json({ error: "cannot delete the last admin", code: "last_admin" }, 400),
    );
    open();
    await press();
    expect(await screen.findByRole("alert")).toHaveTextContent(ERROR_COPY.last_admin);
    expect(calls.filter((c) => c.path === SETTLE)).toEqual([]);
    expect(AdminUsers.settleAttempt).not.toHaveBeenCalled();
    expect(deleteButton()).toBeEnabled();
  });

  it("a non-JSON 200 whose settle says applied ends as deleted, with no counts it was never told", async () => {
    await wire((c) => (c.path === MINT ? json({ attemptId: ID }) : c.method === "DELETE" ? html() : json({ result: "applied" })));
    open();
    await press();
    expect(await screen.findByText("The account was deleted.")).toBeInTheDocument();
    expect(screen.queryByText("Households kept")).not.toBeInTheDocument();
  });

  it("shows the report of a well-formed 200 and sends the id in the body", async () => {
    const calls = await wire((c) =>
      c.path === MINT ? json({ attemptId: ID }) : json({ erasure: { ...noErasure, householdsKept: 4, activityEntriesKept: 6 } }),
    );
    open();
    await press();
    expect(await screen.findByText("The account was deleted.")).toBeInTheDocument();
    const del = calls.find((c) => c.method === "DELETE");
    expect(del?.path).toBe("/admin/users/9");
    expect(del?.body).toMatchObject({ attemptId: ID });
    expect(screen.getByText("4")).toBeInTheDocument();
    expect(calls.filter((c) => c.path === SETTLE)).toEqual([]);
  });
});

describe("DeleteUserPanel when the action or the person changes", () => {
  const other: User = { ...target, id: 12, name: "Otis Other", email: "otis@example.com" };

  function openChangeable(action: "delete" | "disable" = "delete") {
    const onDone = vi.fn();
    const view = render(<DeleteUserPanel target={target} action={action} onDone={onDone} />);
    const change = (next: { target?: User; action?: "delete" | "disable" }) =>
      view.rerender(<DeleteUserPanel target={next.target ?? target} action={next.action ?? action} onDone={onDone} />);
    return { onDone, change };
  }

  const deferred = <T,>() => {
    let resolve!: (v: T) => void;
    const promise = new Promise<T>((res) => {
      resolve = res;
    });
    return { promise, resolve };
  };

  it("sends no DELETE for a mint that finishes after the action became Disable", async () => {
    const mint = deferred<{ attemptId: string }>();
    vi.mocked(AdminUsers.mintAttempt).mockReturnValueOnce(mint.promise);
    const { change } = openChangeable();
    await press();
    await waitFor(() => expect(AdminUsers.mintAttempt).toHaveBeenCalled());
    change({ action: "disable" });
    mint.resolve({ attemptId: "late" });
    await mint.promise;
    await Promise.resolve();
    expect(AdminUsers.remove).not.toHaveBeenCalled();
    // The visible panel is the Disable one, fresh and ready.
    expect(await screen.findByRole("button", { name: "Disable account" })).toBeEnabled();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("drops a settle answer that arrives after the action changed, and shows no result of the old action", async () => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(500, "delete_outcome_unknown"));
    const settle = heldSettle();
    const { onDone, change } = openChangeable();
    await press();
    await screen.findByText(CHECKING);
    change({ action: "disable" });
    expect(screen.queryByText(CHECKING)).not.toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Disable account" })).toBeEnabled();
    settle.answer(settleResult("applied"));
    await Promise.resolve();
    await Promise.resolve();
    expect(screen.queryByText("The account was deleted.")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Disable account" })).toBeEnabled();
    expect(onDone).not.toHaveBeenCalled();
  });

  it("switching back to Delete starts a new panel with a fresh id, not the old outcome", async () => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(500, "delete_outcome_unknown"));
    vi.mocked(AdminUsers.settleAttempt).mockResolvedValueOnce(settleResult("not_applied", true));
    const { change } = openChangeable();
    await press();
    await screen.findByText(ALREADY);
    change({ action: "disable" });
    change({ action: "delete" });
    expect(screen.queryByText(ALREADY)).not.toBeInTheDocument();
    await press();
    await screen.findByText("The account was deleted.");
    expect(removedIds()).toEqual(["attempt-1", "attempt-2"]);
  });

  it("an unresolved outcome for one person does not carry over to another person's panel", async () => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(500, "delete_outcome_unknown"));
    heldSettle();
    const { change } = openChangeable();
    await press();
    await screen.findByText(CHECKING);
    change({ target: other });
    expect(screen.queryByText(CHECKING)).not.toBeInTheDocument();
    await press();
    await screen.findByText("The account was deleted.");
    expect(AdminUsers.remove).toHaveBeenLastCalledWith(12, {}, {}, "attempt-2");
  });

  it("ignores a Disable that finishes after the panel became Delete", async () => {
    const update = deferred<{ user: User }>();
    vi.mocked(AdminUsers.update).mockReturnValueOnce(update.promise);
    const { onDone, change } = openChangeable("disable");
    await waitFor(() => expect(screen.getByRole("button", { name: "Disable account" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Disable account" }));
    await waitFor(() => expect(AdminUsers.update).toHaveBeenCalled());
    change({ action: "delete" });
    update.resolve({ user: target });
    await update.promise;
    await Promise.resolve();
    expect(onDone).not.toHaveBeenCalled();
    expect(await screen.findByRole("button", { name: "Delete account" })).toBeEnabled();
  });
});

describe("DeleteUserPanel keeps Delete off until the list reloads after 'can't tell'", () => {
  const bea: User = { ...target, id: 21, name: "Bea Busy", email: "bea@example.com" };

  async function reachHistory(person: User = target) {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(409, "attempt_expired"));
    vi.mocked(AdminUsers.settleAttempt).mockResolvedValueOnce(settleResult("history_unavailable"));
    const view = render(<DeleteUserPanel target={person} action="delete" onDone={vi.fn()} />);
    await press();
    await screen.findByText(HISTORY);
    return view;
  }

  it("a reopened panel for that person starts blocked, makes no request, and says why", async () => {
    const first = await reachHistory();
    first.unmount();
    vi.mocked(AdminUsers.mintAttempt).mockClear();
    render(<DeleteUserPanel target={target} action="delete" onDone={vi.fn()} />);
    expect(await screen.findByText(HISTORY)).toBeInTheDocument();
    expect(deleteButton()).toBeDisabled();
    expect(AdminUsers.mintAttempt).not.toHaveBeenCalled();
  });

  it("holds through another person's panel replacing it, and does not block that other person", async () => {
    const first = await reachHistory();
    first.unmount();
    const second = render(<DeleteUserPanel target={bea} action="delete" onDone={vi.fn()} />);
    await waitFor(() => expect(deleteButton()).toBeEnabled());
    expect(screen.queryByText(HISTORY)).not.toBeInTheDocument();
    second.unmount();
    render(<DeleteUserPanel target={target} action="delete" onDone={vi.fn()} />);
    expect(await screen.findByText(HISTORY)).toBeInTheDocument();
    expect(deleteButton()).toBeDisabled();
  });

  it("holds through a Close whose reload failed, and releases only after a load that worked", async () => {
    const first = await reachHistory();
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    first.unmount();
    // The parent's reload failed: nothing calls reloadSucceeded.
    render(<DeleteUserPanel target={target} action="delete" onDone={vi.fn()} />);
    expect(await screen.findByText(HISTORY)).toBeInTheDocument();
    expect(deleteButton()).toBeDisabled();
    cleanup();
    // A load that began before the answer proves nothing about it.
    reloadSucceeded(0);
    render(<DeleteUserPanel target={target} action="delete" onDone={vi.fn()} />);
    expect(await screen.findByText(HISTORY)).toBeInTheDocument();
    cleanup();
    reloadSucceeded(reloadStarted());
    render(<DeleteUserPanel target={target} action="delete" onDone={vi.fn()} />);
    await waitFor(() => expect(deleteButton()).toBeEnabled());
    expect(screen.queryByText(HISTORY)).not.toBeInTheDocument();
  });

  it("does not block Disable for that person", async () => {
    const first = await reachHistory();
    first.unmount();
    render(<DeleteUserPanel target={target} action="disable" onDone={vi.fn()} />);
    expect(await screen.findByRole("button", { name: "Disable account" })).toBeEnabled();
  });

  it("is not set by the other outcomes: not_applied leaves the next panel open for a fresh press", async () => {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(500, "delete_outcome_unknown"));
    vi.mocked(AdminUsers.settleAttempt).mockResolvedValueOnce(settleResult("not_applied"));
    const first = render(<DeleteUserPanel target={target} action="delete" onDone={vi.fn()} />);
    await press();
    await screen.findByText(NOTHING);
    first.unmount();
    render(<DeleteUserPanel target={target} action="delete" onDone={vi.fn()} />);
    await waitFor(() => expect(deleteButton()).toBeEnabled());
  });
});

describe("DeleteUserPanel keeps Delete off after a restore until the list reloads", () => {
  const bea: User = { ...target, id: 21, name: "Bea Busy", email: "bea@example.com" };
  const RESTORE = ERROR_COPY.restore_detected;

  async function reachRestore() {
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(503, "restore_detected"));
    const onDone = vi.fn();
    const view = render(<DeleteUserPanel target={target} action="delete" onDone={onDone} />);
    await press();
    await screen.findByText(RESTORE);
    return { view, onDone };
  }

  const reopen = (person: User = target, action: "delete" | "disable" = "delete") =>
    render(<DeleteUserPanel target={person} action={action} onDone={vi.fn()} />);

  it("closing asks for a reload, and a reopened panel is blocked with the restore wording and sends nothing", async () => {
    const { view, onDone } = await reachRestore();
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onDone).toHaveBeenCalledWith(true);
    view.unmount();
    vi.mocked(AdminUsers.mintAttempt).mockClear();
    reopen();
    expect(await screen.findByRole("alert")).toHaveTextContent(RESTORE);
    expect(deleteButton()).toBeDisabled();
    expect(AdminUsers.mintAttempt).not.toHaveBeenCalled();
    expect(document.body).not.toHaveTextContent(HISTORY);
  });

  it("holds for another person and through an action switch to Disable and back", async () => {
    const { view } = await reachRestore();
    view.unmount();
    const other = reopen(bea);
    expect(await screen.findByRole("alert")).toHaveTextContent(RESTORE);
    expect(deleteButton()).toBeDisabled();
    other.unmount();
    const switching = render(<DeleteUserPanel target={target} action="disable" onDone={vi.fn()} />);
    expect(await screen.findByRole("button", { name: "Disable account" })).toBeEnabled();
    switching.rerender(<DeleteUserPanel target={target} action="delete" onDone={vi.fn()} />);
    expect(await screen.findByRole("alert")).toHaveTextContent(RESTORE);
    expect(deleteButton()).toBeDisabled();
  });

  it("holds through a failed refresh and an earlier in-flight one, and lifts only for a load that began after it and worked", async () => {
    const earlier = reloadStarted();
    const { view } = await reachRestore();
    view.unmount();
    // A refresh that failed calls nothing; one that began before the restore proves nothing.
    reloadSucceeded(earlier);
    reopen();
    expect(await screen.findByRole("alert")).toHaveTextContent(RESTORE);
    expect(deleteButton()).toBeDisabled();
    cleanup();
    reloadSucceeded(reloadStarted());
    reopen();
    await waitFor(() => expect(deleteButton()).toBeEnabled());
    expect(screen.queryByText(RESTORE)).not.toBeInTheDocument();
  });

  it("is also recorded when the mint or the settle answers restore_detected", async () => {
    vi.mocked(AdminUsers.mintAttempt).mockRejectedValueOnce(coded(503, "restore_detected"));
    const first = reopen();
    await press();
    await screen.findByText(RESTORE);
    first.unmount();
    reopen();
    expect(await screen.findByRole("alert")).toHaveTextContent(RESTORE);
    cleanup();
    reloadSucceeded(reloadStarted());
    vi.mocked(AdminUsers.remove).mockRejectedValueOnce(coded(500, "delete_outcome_unknown"));
    vi.mocked(AdminUsers.settleAttempt).mockRejectedValueOnce(coded(503, "restore_detected"));
    const second = reopen();
    await press();
    await screen.findByText(RESTORE);
    expect(screen.getByRole("button", { name: "Check again" })).toBeEnabled();
    second.unmount();
    reopen();
    expect(await screen.findByRole("alert")).toHaveTextContent(RESTORE);
    expect(deleteButton()).toBeDisabled();
  });
});
