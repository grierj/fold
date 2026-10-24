import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { User } from "../types";

const actor = vi.hoisted(() => ({ user: null as User | null, refresh: vi.fn() }));
const chunk = vi.hoisted(() => {
  let finish = () => {};
  const ready = new Promise<void>((resolve) => { finish = resolve; });
  return { ready, finish: () => finish() };
});
vi.mock("../components/HouseholdCard", async () => {
  await chunk.ready;
  return { HouseholdCard: () => <div>Household loaded</div> };
});

vi.mock("../components/GuardianConsentCard", () => ({ GuardianConsentCard: () => null }));

const get = vi.fn();
const create = vi.fn();
const setScope = vi.fn();
vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  CalendarFeeds: { get: () => get(), create: (s: string) => create(s), setScope: (s: string) => setScope(s), revoke: vi.fn() },
  Profile: { update: vi.fn() },
}));
vi.mock("../auth", () => ({ useAuth: () => ({ user: actor.user, loading: false, refresh: actor.refresh, logout: vi.fn(), isAdmin: false }) }));

import { ApiError, Profile } from "../api";
import { CalendarFeedCard, ProfilePage } from "./Profile";

const renderCard = () => render(<MemoryRouter><CalendarFeedCard /></MemoryRouter>);

describe("CalendarFeedCard (M17-REVIEW)", () => {
  beforeEach(() => {
    get.mockReset();
    create.mockReset();
    setScope.mockReset();
  });

  it("shows a skeleton while loading, not the no-link copy", () => {
    get.mockReturnValue(new Promise(() => {}));
    renderCard();
    expect(screen.getByText("Loading your calendar link…")).toBeInTheDocument();
    expect(screen.queryByText(/don.t have a calendar link/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Create calendar link" })).toBeNull();
  });

  it("a failed load offers Try again and never Create (M1)", async () => {
    get.mockRejectedValueOnce(new ApiError(500, "boom"));
    get.mockResolvedValueOnce({ calendarFeed: { hasToken: true, scope: "mine" } });
    renderCard();
    await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
    expect(screen.queryByRole("button", { name: "Create calendar link" })).toBeNull();
    expect(await screen.findByRole("button", { name: "Make a new link" })).toBeInTheDocument();
  });

  it("a fresh link makes Copy the primary action (M4)", async () => {
    get.mockResolvedValue({ calendarFeed: { hasToken: false, scope: "mine" } });
    create.mockResolvedValue({ calendarFeed: { hasToken: true, scope: "mine", url: "https://x/cal/u/t.ics" } });
    renderCard();
    await userEvent.click(await screen.findByRole("button", { name: "Create calendar link" }));
    expect(await screen.findByRole("button", { name: "Copy link" })).toHaveClass("btn-primary");
    expect(screen.getByRole("button", { name: "Make a new link" })).toHaveClass("btn-secondary");
  });

  it("a failed scope save puts the radio back (N2)", async () => {
    get.mockResolvedValue({ calendarFeed: { hasToken: true, scope: "mine" } });
    setScope.mockRejectedValue(new ApiError(500, "boom"));
    renderCard();
    await userEvent.click(await screen.findByLabelText(/Everything I can see/));
    expect(await screen.findByLabelText(/My events/)).toBeChecked();
  });
});

it("reserves household card height while its lazy chunk is slow", async () => {
  actor.user = { id: 1, name: "Pat Smith", displayName: "Pat", email: "pat@example.com", phone: "", avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", status: "active", hasPassword: true, accountType: "adult", hideContactFromLeaders: false, timezone: "America/Boise" };
  get.mockResolvedValue({ calendarFeed: { hasToken: false, scope: "mine" } });
  render(<MemoryRouter><ProfilePage /></MemoryRouter>);
  expect(screen.getByRole("region", { name: "My household loading" })).toHaveClass("min-h-80");
  await act(async () => { chunk.finish(); await chunk.ready; });
  expect(await screen.findByText("Household loaded")).toBeInTheDocument();
  actor.user = null;
});

const adult: User = { id: 1, name: "Pat Smith", displayName: "Pat", email: "pat@example.com", phone: "208-555-0100", hideContactFromLeaders: false, avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", status: "active", hasPassword: true, accountType: "adult", timezone: "America/Boise" };
const privacyLabel = /^Hide my contact info from group leaders/;
const renderProfile = () => render(<MemoryRouter><ProfilePage /></MemoryRouter>);

describe("ProfilePage contact privacy", () => {
  beforeEach(() => {
    actor.user = { ...adult };
    actor.refresh.mockReset().mockResolvedValue(undefined);
    vi.mocked(Profile.update).mockReset().mockResolvedValue({ user: adult });
    get.mockResolvedValue({ calendarFeed: { hasToken: false, scope: "mine" } });
  });

  it("defaults off for adults and saves that choice with the other profile fields", async () => {
    renderProfile();
    expect(screen.getByRole("checkbox", { name: privacyLabel })).not.toBeChecked();
    await userEvent.click(screen.getByRole("button", { name: "Save profile" }));
    expect(Profile.update).toHaveBeenCalledWith({ displayName: "Pat", name: "Pat Smith", phone: adult.phone, timezone: "America/Boise", hideContactFromLeaders: false });
    expect(actor.refresh).toHaveBeenCalledOnce();
    expect(await screen.findByText("Profile saved.")).toBeInTheDocument();
  });

  it("saves a hidden adult choice and can turn it back off", async () => {
    actor.user = { ...adult, hideContactFromLeaders: true };
    renderProfile();
    const toggle = screen.getByRole("checkbox", { name: privacyLabel });
    expect(toggle).toBeChecked();
    await userEvent.click(screen.getByRole("button", { name: "Save profile" }));
    expect(Profile.update).toHaveBeenLastCalledWith(expect.objectContaining({ hideContactFromLeaders: true }));
    await userEvent.click(toggle);
    await userEvent.click(screen.getByRole("button", { name: "Save profile" }));
    expect(Profile.update).toHaveBeenLastCalledWith(expect.objectContaining({ hideContactFromLeaders: false }));
  });

  it("keeps an adult's changed choice after a failed save and uses friendly copy", async () => {
    vi.mocked(Profile.update).mockRejectedValueOnce(new ApiError(500, "private server text"));
    renderProfile();
    await userEvent.click(screen.getByRole("checkbox", { name: privacyLabel }));
    await userEvent.click(screen.getByRole("button", { name: "Save profile" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't save your changes.");
    expect(screen.getByRole("checkbox", { name: privacyLabel })).toBeChecked();
    expect(screen.queryByText("private server text")).not.toBeInTheDocument();
    expect(actor.refresh).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Save profile" }));
    expect(await screen.findByText("Profile saved.")).toBeInTheDocument();
    expect(Profile.update).toHaveBeenLastCalledWith(expect.objectContaining({ hideContactFromLeaders: true }));
  });

  it("explains always-hidden teen contact without an editable preference or avatar controls", async () => {
    actor.user = { ...adult, accountType: "teen", hideContactFromLeaders: true };
    renderProfile();
    expect(screen.getByText(/Your email and phone are always hidden from group leaders and event hosts/)).toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: privacyLabel })).not.toBeInTheDocument();
    expect(screen.queryByText("Profile picture")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove picture" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Save profile" }));
    expect(Profile.update).toHaveBeenCalledWith({ displayName: "Pat", name: "Pat Smith", phone: adult.phone, timezone: "America/Boise" });
    expect(await screen.findByText("Profile saved.")).toBeInTheDocument();
  });
});
