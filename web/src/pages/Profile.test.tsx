import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
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
vi.mock("../components/GuardianConsentCard", () => ({ GuardianConsentCard: () => <div>Guardian links loaded</div> }));
const get = vi.fn();
vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  CalendarFeeds: { get: () => get(), create: vi.fn(), setScope: vi.fn(), revoke: vi.fn() },
  Profile: { update: vi.fn(), changePassword: vi.fn() },
}));
vi.mock("../auth", () => ({ useAuth: () => ({ user: actor.user, loading: false, refresh: actor.refresh, logout: vi.fn(), isAdmin: false }) }));

import { ApiError, Profile } from "../api";
import { ProfilePage } from "./Profile";
const adult: User = { id: 1, name: "Pat Smith", displayName: "Pat", email: "pat@example.com", phone: "208-555-0100", hideContactFromLeaders: false, avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", status: "active", hasPassword: true, accountType: "adult", timezone: "America/Boise" };
const privacyLabel = /^Hide my contact info from group leaders/;
const renderProfile = (path = "/profile") => render(<MemoryRouter initialEntries={[path]}><Routes><Route path="/profile/:section?" element={<ProfilePage />} /></Routes></MemoryRouter>);

beforeEach(() => {
  actor.user = { ...adult };
  actor.refresh.mockReset().mockResolvedValue(undefined);
  vi.mocked(Profile.update).mockReset().mockResolvedValue({ user: adult });
  vi.mocked(Profile.changePassword).mockReset().mockResolvedValue({});
  get.mockReset().mockResolvedValue({ calendarFeed: { hasToken: false, scope: "mine" } });
});

it("opens a short hub without mounting forms or fetching the calendar feed", () => {
  renderProfile();
  expect(screen.getByRole("heading", { level: 1, name: "Your profile" })).toBeInTheDocument();
  expect(screen.getByText("Pat")).toBeInTheDocument();
  for (const name of ["Profile details", "Privacy", "Email and password", "Calendar feed", "Household", "Parent or guardian link", "Invite someone", "Requests"]) {
    expect(screen.getByRole("link", { name })).toBeInTheDocument();
  }
  expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  expect(screen.queryByText("Household loaded")).not.toBeInTheDocument();
  expect(screen.queryByText("Guardian links loaded")).not.toBeInTheDocument();
  expect(get).not.toHaveBeenCalled();
});

it("loads only the chosen setting, supports a bookmarked page and returns to the hub", async () => {
  renderProfile("/profile/calendar");
  expect(screen.getByRole("heading", { level: 1, name: "Calendar feed" })).toBeInTheDocument();
  await screen.findByRole("button", { name: "Create calendar link" });
  expect(get).toHaveBeenCalledOnce();
  expect(screen.queryByLabelText("Full name")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("New email")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("link", { name: "Back to your profile" }));
  expect(screen.getByRole("heading", { level: 1, name: "Your profile" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Create calendar link" })).not.toBeInTheDocument();
});

it("shows one security form at a time and clears a password draft when leaving it", async () => {
  renderProfile("/profile/security?tab=password");
  expect(screen.queryByLabelText("New email")).not.toBeInTheDocument();
  await userEvent.type(screen.getByLabelText("Current password"), "do not retain this");
  await userEvent.click(screen.getByRole("button", { name: "Email" }));
  expect(screen.queryByLabelText("Current password")).not.toBeInTheDocument();
  expect(screen.getByLabelText("New email")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Password" }));
  expect(screen.getByLabelText("Current password")).toHaveValue("");
  expect(Profile.update).not.toHaveBeenCalled();
  expect(Profile.changePassword).not.toHaveBeenCalled();
});

it("reserves household height only when its section is opened and the lazy chunk is slow", async () => {
  renderProfile("/profile/household");
  expect(screen.getByRole("region", { name: "My household loading" })).toHaveClass("min-h-80");
  await act(async () => { chunk.finish(); await chunk.ready; });
  expect(await screen.findByText("Household loaded")).toBeInTheDocument();
});

it("saves details without silently submitting a privacy choice", async () => {
  renderProfile("/profile/details");
  expect(screen.queryByRole("checkbox", { name: privacyLabel })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Save profile" }));
  expect(Profile.update).toHaveBeenCalledWith({ displayName: "Pat", name: "Pat Smith", phone: adult.phone, timezone: "America/Boise" });
  expect(actor.refresh).toHaveBeenCalledOnce();
  expect(await screen.findByText("Profile saved.")).toBeInTheDocument();
});

it("defaults adult privacy off and saves only that choice", async () => {
  renderProfile("/profile/privacy");
  expect(screen.getByRole("checkbox", { name: privacyLabel })).not.toBeChecked();
  await userEvent.click(screen.getByRole("button", { name: "Save privacy" }));
  expect(Profile.update).toHaveBeenCalledWith({ hideContactFromLeaders: false });
  expect(actor.refresh).toHaveBeenCalledOnce();
  expect(await screen.findByText("Privacy saved.")).toBeInTheDocument();
});

it("saves a hidden adult choice and can turn it back off", async () => {
  actor.user = { ...adult, hideContactFromLeaders: true };
  renderProfile("/profile/privacy");
  const toggle = screen.getByRole("checkbox", { name: privacyLabel });
  expect(toggle).toBeChecked();
  await userEvent.click(screen.getByRole("button", { name: "Save privacy" }));
  expect(Profile.update).toHaveBeenLastCalledWith({ hideContactFromLeaders: true });
  await userEvent.click(toggle);
  await userEvent.click(screen.getByRole("button", { name: "Save privacy" }));
  expect(Profile.update).toHaveBeenLastCalledWith({ hideContactFromLeaders: false });
});

it("keeps an adult privacy draft after a failed save and never exposes server text", async () => {
  vi.mocked(Profile.update).mockRejectedValueOnce(new ApiError(500, "private server text"));
  renderProfile("/profile/privacy");
  await userEvent.click(screen.getByRole("checkbox", { name: privacyLabel }));
  await userEvent.click(screen.getByRole("button", { name: "Save privacy" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't save your changes.");
  expect(screen.getByRole("checkbox", { name: privacyLabel })).toBeChecked();
  expect(screen.queryByText("private server text")).not.toBeInTheDocument();
  expect(actor.refresh).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Save privacy" }));
  expect(await screen.findByText("Privacy saved.")).toBeInTheDocument();
  expect(Profile.update).toHaveBeenLastCalledWith({ hideContactFromLeaders: true });
});

it("explains immutable teen privacy without editing or saving it", () => {
  actor.user = { ...adult, accountType: "teen", hideContactFromLeaders: true };
  renderProfile("/profile/privacy");
  expect(screen.getByText(/Your email and phone are always hidden from group leaders and event hosts/)).toBeInTheDocument();
  expect(screen.queryByRole("checkbox", { name: privacyLabel })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Save privacy" })).not.toBeInTheDocument();
  expect(Profile.update).not.toHaveBeenCalled();
});

it("keeps teen details editable without avatar controls or a privacy payload", async () => {
  actor.user = { ...adult, accountType: "teen", hideContactFromLeaders: true };
  renderProfile("/profile/details");
  expect(screen.queryByText("Profile picture")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Remove picture" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Save profile" }));
  expect(Profile.update).toHaveBeenCalledWith({ displayName: "Pat", name: "Pat Smith", phone: adult.phone, timezone: "America/Boise" });
});

it.each(["household", "guardian", "invite", "unknown"])("returns a teen's unavailable %s deep link to their hub", (section) => {
  actor.user = { ...adult, accountType: "teen", hideContactFromLeaders: true };
  renderProfile(`/profile/${section}`);
  expect(screen.getByRole("heading", { level: 1, name: "Your profile" })).toBeInTheDocument();
  for (const name of ["Household", "Parent or guardian link", "Invite someone"]) expect(screen.queryByRole("link", { name })).not.toBeInTheDocument();
  expect(screen.queryByText("Household loaded")).not.toBeInTheDocument();
  expect(screen.queryByText("Guardian links loaded")).not.toBeInTheDocument();
  expect(Profile.update).not.toHaveBeenCalled();
});
