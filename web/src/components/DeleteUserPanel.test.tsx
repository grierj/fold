import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AdminUsers, ApiError } from "../api";
import { ERROR_COPY } from "../lib/errors";
import type { HandoffCandidate, SoleHostedEvent, SoleHostedEventsResponse, SoleLedGroup, User } from "../types";
import { DeleteUserPanel } from "./DeleteUserPanel";

vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 1, name: "Grier" } }) }));
vi.mock("../api", async (importActual) => ({
  ...(await importActual<typeof import("../api")>()),
  AdminUsers: { soleLeaderGroups: vi.fn(), soleHostEvents: vi.fn(), remove: vi.fn(), update: vi.fn() },
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

const eventSelect = (title: string) => screen.getByRole("combobox", { name: `New host for ${title}` });
const deleteButton = () => screen.getByRole("button", { name: "Delete account" });
const staleError = (data: Record<string, unknown>, code: string) => new ApiError(400, "stale", { code, ...data });

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(AdminUsers.soleLeaderGroups).mockResolvedValue({ groups: [] });
  vi.mocked(AdminUsers.soleHostEvents).mockResolvedValue(hosted([], []));
  vi.mocked(AdminUsers.remove).mockResolvedValue({
    erasure: { householdsKept: 0, householdsScrubbed: 0, peopleAnonymized: 0, guardianLinksRevoked: 0, activityEntriesKept: 0 },
  });
});
afterEach(cleanup);

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
    expect(AdminUsers.remove).toHaveBeenCalledWith(9, {}, {});
    await waitFor(() => expect(onDone).toHaveBeenCalledWith(true));
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
    expect(AdminUsers.remove).toHaveBeenCalledWith(9, { "5": 1 }, { "10": 2, "11": 1 });
    await waitFor(() => expect(onDone).toHaveBeenCalledWith(true));
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
    await waitFor(() => expect(onDone).toHaveBeenCalledWith(true));
  });
});
