import { act, render, screen, within } from "@testing-library/react";
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
  NotificationSettings: { get: () => Promise.resolve({ settings: { optionalEmailOff: true, kinds: [{ category: "groups", off: true }] } }), update: vi.fn() },
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
  for (const name of ["Profile details", "Privacy", "Notifications", "Email and password", "Calendar feed", "Household", "Parent or guardian link", "Invite someone", "Contact and add requests"]) {
    expect(screen.getByRole("link", { name: new RegExp(`^${name}`) })).toBeInTheDocument();
  }
  expect(screen.queryByRole("link", { name: "Requests" })).not.toBeInTheDocument();
  expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  expect(screen.queryByText("Household loaded")).not.toBeInTheDocument();
  expect(screen.queryByText("Guardian links loaded")).not.toBeInTheDocument();
  expect(get).not.toHaveBeenCalled();
});

it("groups the rows under You, Family and Sharing, each with a one-line description", () => {
  renderProfile();
  const rows: Record<string, [string, string][]> = {
    You: [["Profile details", "Name, photo and time zone"], ["Email and password", "How you sign in"], ["Privacy", "Hide your contact info from leaders"], ["Notifications", "Turn optional email on or off"]],
    Family: [["Household", "Family members you sign up with you"], ["Parent or guardian link", "Connect a parent or guardian"]],
    Sharing: [["Calendar feed", "Add Fold dates to your phone's calendar"], ["Invite someone", "Ask an admin to let someone in"], ["Contact and add requests", "Requests to reach or add you"]],
  };
  expect(screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent)).toEqual(["You", "Family", "Sharing"]);
  for (const [heading, links] of Object.entries(rows)) {
    const group = screen.getByRole("group", { name: heading });
    expect(within(group).getAllByRole("link").map((l) => l.textContent?.replace("›", ""))).toEqual(links.map(([label, description]) => `${label} ${description}`));
    for (const [label, description] of links) expect(within(group).getByRole("link", { name: `${label} ${description}` })).toBeInTheDocument();
  }
});

it("hides the Family group from a teen", () => {
  actor.user = { ...adult, accountType: "teen" };
  renderProfile();
  expect(screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent)).toEqual(["You", "Sharing"]);
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
  const passwordCurrent = screen.getByLabelText("Current password");
  await userEvent.type(passwordCurrent, "do not retain this");
  await userEvent.click(screen.getByRole("button", { name: "Email" }));
  expect(passwordCurrent).not.toBeInTheDocument();
  expect(screen.getByLabelText("New email")).toBeInTheDocument();
  expect(screen.getByLabelText("Current password")).toHaveValue("");
  expect(screen.queryByLabelText("New password")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Password" }));
  expect(screen.getByLabelText("Current password")).toHaveValue("");
  expect(Profile.update).not.toHaveBeenCalled();
  expect(Profile.changePassword).not.toHaveBeenCalled();
});

it("sends the current password for an email change and clears only the secret after failure", async () => {
  vi.mocked(Profile.update)
    .mockRejectedValueOnce(new ApiError(400, "private server text", { code: "wrong_password" }))
    .mockResolvedValueOnce({ user: adult, verificationSent: true });
  renderProfile("/profile/security?tab=email");
  const email = screen.getByLabelText("New email");
  const password = screen.getByLabelText("Current password");
  await userEvent.type(email, "new@example.com");
  await userEvent.type(password, " secret with spaces ");
  await userEvent.click(screen.getByRole("button", { name: "Send verification link" }));

  expect(Profile.update).toHaveBeenLastCalledWith({ email: "new@example.com", currentPassword: " secret with spaces " });
  expect(await screen.findByRole("alert")).toHaveTextContent("Your current password isn't right.");
  expect(email).toHaveValue("new@example.com");
  expect(password).toHaveValue("");
  expect(screen.queryByText("private server text")).not.toBeInTheDocument();

  await userEvent.type(password, "correct password");
  await userEvent.click(screen.getByRole("button", { name: "Send verification link" }));
  expect(await screen.findByText("Verification link sent to new@example.com. It expires in 24 hours.")).toBeInTheDocument();
  expect(email).toHaveValue("");
  expect(password).toHaveValue("");
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

it("saves the privacy choice the moment it changes, with no Save button", async () => {
  renderProfile("/profile/privacy");
  const toggle = screen.getByRole("checkbox", { name: privacyLabel });
  expect(toggle).not.toBeChecked();
  expect(screen.queryByRole("button", { name: "Save privacy" })).not.toBeInTheDocument();
  expect(screen.queryByText(/Save your privacy choice/)).not.toBeInTheDocument();
  expect(Profile.update).not.toHaveBeenCalled();
  await userEvent.click(toggle);
  expect(Profile.update).toHaveBeenCalledWith({ hideContactFromLeaders: true });
  expect(await screen.findByText("Saved")).toBeInTheDocument();
  expect(screen.getByRole("status")).toHaveTextContent("Saved");
  expect(actor.refresh).toHaveBeenCalledOnce();
  expect(toggle).toBeChecked();
});

it("saves turning a hidden choice back off", async () => {
  actor.user = { ...adult, hideContactFromLeaders: true };
  renderProfile("/profile/privacy");
  const toggle = screen.getByRole("checkbox", { name: privacyLabel });
  expect(toggle).toBeChecked();
  await userEvent.click(toggle);
  expect(Profile.update).toHaveBeenLastCalledWith({ hideContactFromLeaders: false });
  expect(await screen.findByText("Saved")).toBeInTheDocument();
});

it("puts the privacy box back and says what went wrong when saving fails, without server text", async () => {
  vi.mocked(Profile.update).mockRejectedValueOnce(new ApiError(500, "private server text"));
  renderProfile("/profile/privacy");
  await userEvent.click(screen.getByRole("checkbox", { name: privacyLabel }));
  expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't save your changes.");
  expect(screen.getByRole("checkbox", { name: privacyLabel })).not.toBeChecked();
  expect(screen.queryByText("Saved")).not.toBeInTheDocument();
  expect(screen.queryByText("private server text")).not.toBeInTheDocument();
  expect(actor.refresh).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("checkbox", { name: privacyLabel }));
  expect(await screen.findByText("Saved")).toBeInTheDocument();
  expect(Profile.update).toHaveBeenLastCalledWith({ hideContactFromLeaders: true });
});

it("disables the privacy box while it saves, so a second change waits for the first", async () => {
  let finish: (v: { user: User }) => void = () => {};
  vi.mocked(Profile.update).mockReturnValue(new Promise((resolve) => { finish = resolve; }));
  renderProfile("/profile/privacy");
  const toggle = screen.getByRole("checkbox", { name: privacyLabel });
  await userEvent.click(toggle);
  expect(toggle).toBeDisabled();
  await userEvent.click(toggle);
  expect(Profile.update).toHaveBeenCalledOnce();
  await act(async () => { finish({ user: adult }); });
  expect(await screen.findByText("Saved")).toHaveClass("text-success");
  expect(toggle).toBeEnabled();
});

it("does not count trailing spaces as unsaved profile edits", async () => {
  renderProfile("/profile/details");
  const closing = () => { const e = new Event("beforeunload", { cancelable: true }); window.dispatchEvent(e); return e.defaultPrevented; };
  await userEvent.type(screen.getByLabelText("Full name"), "   ");
  expect(closing()).toBe(false);
  await userEvent.type(screen.getByLabelText("Full name"), "x");
  expect(closing()).toBe(true);
});

it("lists Full name first, then an optional Preferred name with its hint, and shows initials until there is a photo", () => {
  renderProfile("/profile/details");
  const boxes = screen.getAllByRole("textbox").map((el) => el.id);
  expect(boxes.indexOf("profile-full-name")).toBeLessThan(boxes.indexOf("profile-display-name"));
  const preferred = screen.getByLabelText(/^Preferred name/);
  expect(preferred).toHaveValue("Pat");
  expect(preferred).toHaveAccessibleDescription("We’ll use this in lists, for example Bob instead of Robert.");
  expect(screen.queryByLabelText(/^Display name/)).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Choose photo" })).toBeInTheDocument();
  expect(screen.getByText("P")).toBeInTheDocument();
  expect(screen.queryByText("Tap or click the image to move the focal point — faces stay centered when cropped.")).not.toBeInTheDocument();
  expect(screen.queryByRole("link", { name: "Change your sign-in email" })).not.toBeInTheDocument();
});

it("saves a changed preferred name under the same API key", async () => {
  renderProfile("/profile/details");
  const preferred = screen.getByLabelText(/^Preferred name/);
  await userEvent.clear(preferred);
  await userEvent.type(preferred, "Patty");
  await userEvent.click(screen.getByRole("button", { name: "Save profile" }));
  expect(Profile.update).toHaveBeenCalledWith({ displayName: "Patty", name: "Pat Smith", phone: adult.phone, timezone: "America/Boise" });
});

it("asks before the tab closes while profile details have unsaved edits", async () => {
  renderProfile("/profile/details");
  const closing = () => { const e = new Event("beforeunload", { cancelable: true }); window.dispatchEvent(e); return e.defaultPrevented; };
  expect(closing()).toBe(false);
  await userEvent.type(screen.getByLabelText("Full name"), "x");
  expect(closing()).toBe(true);
});

it("shows the invite hints and the review line", () => {
  renderProfile("/profile/invite");
  expect(screen.getByText("Ask for an account for a friend, who only hears from us once it’s approved.")).toBeInTheDocument();
  expect(screen.getByText("Teen and child accounts need a parent or guardian.")).toBeInTheDocument();
  expect(screen.getByText("An admin reviews it first.")).toBeInTheDocument();
});

it("embeds the household under its own h1 without a second heading", async () => {
  renderProfile("/profile/household");
  await act(async () => { chunk.finish(); await chunk.ready; });
  expect(await screen.findByText("Household loaded")).toBeInTheDocument();
  expect(screen.getAllByRole("heading", { level: 1 }).map((h) => h.textContent)).toEqual(["Household"]);
  expect(screen.queryByRole("heading", { name: "My household" })).not.toBeInTheDocument();
});

it("explains immutable teen privacy without editing or saving it", () => {
  actor.user = { ...adult, accountType: "teen", hideContactFromLeaders: true };
  renderProfile("/profile/privacy");
  expect(screen.getByText(/Your email and phone are always hidden from group leaders and event hosts/)).toBeInTheDocument();
  expect(screen.queryByRole("checkbox", { name: privacyLabel })).not.toBeInTheDocument();
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
  for (const name of ["Household", "Parent or guardian link", "Invite someone"]) expect(screen.queryByRole("link", { name: new RegExp(`^${name}`) })).not.toBeInTheDocument();
  expect(screen.queryByText("Household loaded")).not.toBeInTheDocument();
  expect(screen.queryByText("Guardian links loaded")).not.toBeInTheDocument();
  expect(Profile.update).not.toHaveBeenCalled();
});

it.each(["adult", "teen"] as const)("shows Notifications at /profile/notifications to a %s", async (accountType) => {
  actor.user = { ...adult, accountType };
  renderProfile("/profile/notifications");
  expect(screen.getByRole("heading", { level: 1, name: "Notifications" })).toBeInTheDocument();
  expect(await screen.findByRole("checkbox", { name: /^Turn off all optional email/ })).toBeChecked();
});
