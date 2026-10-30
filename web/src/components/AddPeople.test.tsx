import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { ApiError, Groups, Invitations, People } from "../api";
import { AddPeople } from "./AddPeople";

vi.mock("../api", async (actual) => ({
  ...(await actual<typeof import("../api")>()),
  People: { search: vi.fn() },
  Groups: { addMember: vi.fn() },
  Invitations: { create: vi.fn() },
}));

const pat = { selectionId: "opaque-pat", displayName: "Pat Lee", teen: false, sharedGroups: [], otherGroups: [] };
const onAdded = vi.fn();
function panel(over: Partial<React.ComponentProps<typeof AddPeople>> = {}) {
  render(<MemoryRouter><AddPeople groupId={1} groupName="Choir" memberCount={2} memberLimit={null} canSearch onAdded={onAdded} {...over} /></MemoryRouter>);
}
async function openPanel() { await userEvent.click(screen.getByRole("button", { name: "Add people" })); }
async function pickPat() {
  await userEvent.type(screen.getByRole("combobox", { name: "Find a person by name" }), "Pat");
  await userEvent.click(await screen.findByRole("option"));
}
async function useEmail(address: string) {
  await userEvent.click(screen.getByRole("button", { name: "Use an email instead" }));
  await userEvent.type(screen.getByLabelText("Email address"), address);
}
const noAccount = () => new ApiError(404, "user not found", { code: "not_found" });

beforeEach(() => {
  vi.resetAllMocks();
  onAdded.mockResolvedValue(undefined);
  vi.mocked(People.search).mockResolvedValue({ state: "results", people: [pat] });
});

it("is one closed button until opened, then starts with the name search", async () => {
  panel();
  expect(screen.getByRole("button", { name: "Add people" })).toHaveAttribute("aria-expanded", "false");
  expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
  await openPanel();
  expect(screen.getByRole("button", { name: "Add people" })).toHaveAttribute("aria-expanded", "true");
  expect(screen.getByRole("combobox", { name: "Find a person by name" })).toBeInTheDocument();
  expect(screen.queryByLabelText("Email address")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Use an email instead" })).toBeInTheDocument();
});

it("adds the person chosen by name and refreshes the roster", async () => {
  vi.mocked(Groups.addMember).mockResolvedValue({});
  panel(); await openPanel(); await pickPat();
  await userEvent.click(screen.getByRole("button", { name: "Add member" }));
  expect(Groups.addMember).toHaveBeenCalledExactlyOnceWith(1, { selectionId: "opaque-pat" });
  expect(await screen.findByText("Added Pat Lee.")).toHaveAttribute("role", "status");
  expect(onAdded).toHaveBeenCalledTimes(1);
});

it("offers Ask to add for a teen instead of adding directly", async () => {
  vi.mocked(People.search).mockResolvedValue({ state: "results", people: [{ ...pat, displayName: "Teen Pat", teen: true }] });
  panel(); await openPanel(); await pickPat();
  await userEvent.click(screen.getByRole("button", { name: "Ask to add" }));
  expect(screen.getByRole("button", { name: "Send ask to add" })).toBeInTheDocument();
  expect(Groups.addMember).not.toHaveBeenCalled();
});

it("switches to one email field and adds an existing account", async () => {
  vi.mocked(Groups.addMember).mockResolvedValue({});
  panel(); await openPanel(); await useEmail("pat@example.com");
  expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Add member" }));
  expect(Groups.addMember).toHaveBeenCalledExactlyOnceWith(1, "pat@example.com");
  expect(await screen.findByText("Added pat@example.com.")).toBeInTheDocument();
  expect(Invitations.create).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Search by name instead" }));
  expect(screen.getByRole("combobox", { name: "Find a person by name" })).toBeInTheDocument();
});

it("offers the invitation when no account uses the email, and sends it to that email", async () => {
  vi.mocked(Groups.addMember).mockRejectedValue(noAccount());
  vi.mocked(Invitations.create).mockResolvedValue({ invitation: {} } as Awaited<ReturnType<typeof Invitations.create>>);
  panel(); await openPanel(); await useEmail("new@example.com");
  await userEvent.click(screen.getByRole("button", { name: "Add member" }));
  expect(await screen.findByText("No account uses that email. Invite them to Fold?")).toBeInTheDocument();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Send invitation" }));
  expect(Invitations.create).toHaveBeenCalledExactlyOnceWith({ email: "new@example.com", groupId: 1, overLimit: false });
  expect(await screen.findByText("Invitation sent to new@example.com.")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Send invitation" })).not.toBeInTheDocument();
  expect(screen.getByLabelText("Email address")).toHaveValue("");
});

it("drops the invitation offer when the email changes", async () => {
  vi.mocked(Groups.addMember).mockRejectedValue(noAccount());
  panel(); await openPanel(); await useEmail("new@example.com");
  await userEvent.click(screen.getByRole("button", { name: "Add member" }));
  await screen.findByRole("button", { name: "Send invitation" });
  await userEvent.type(screen.getByLabelText("Email address"), "x");
  expect(screen.queryByRole("button", { name: "Send invitation" })).not.toBeInTheDocument();
});

it("keeps the invitation rules: a refusal shows friendly wording, never the server's text", async () => {
  vi.mocked(Groups.addMember).mockRejectedValue(noAccount());
  vi.mocked(Invitations.create).mockRejectedValue(new ApiError(429, "invitation limit reached (20 per day)", { code: "rate_limited" }));
  panel(); await openPanel(); await useEmail("new@example.com");
  await userEvent.click(screen.getByRole("button", { name: "Add member" }));
  await userEvent.click(await screen.findByRole("button", { name: "Send invitation" }));
  expect(await screen.findByRole("alert")).not.toHaveTextContent("20 per day");
});

it("asks before adding by email over the member limit (DEC-39)", async () => {
  vi.mocked(Groups.addMember).mockRejectedValueOnce(new ApiError(409, "full", { code: "group_full" })).mockResolvedValueOnce({});
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
  panel({ memberCount: 3, memberLimit: 3 }); await openPanel(); await useEmail("pat@example.com");
  await userEvent.click(screen.getByRole("button", { name: "Add member" }));
  await waitFor(() => expect(Groups.addMember).toHaveBeenCalledTimes(2));
  expect(confirm).toHaveBeenCalledWith("“Choir” is full (3 of 3). Add pat@example.com anyway?");
  expect(Groups.addMember).toHaveBeenLastCalledWith(1, "pat@example.com", true);
});

it("adds nobody when the leader declines the over-limit prompt", async () => {
  vi.mocked(Groups.addMember).mockRejectedValue(new ApiError(409, "full", { code: "group_full" }));
  vi.spyOn(window, "confirm").mockReturnValue(false);
  panel({ memberCount: 3, memberLimit: 3 }); await openPanel(); await pickPat();
  await userEvent.click(screen.getByRole("button", { name: "Add member" }));
  await waitFor(() => expect(window.confirm).toHaveBeenCalled());
  expect(Groups.addMember).toHaveBeenCalledTimes(1);
  expect(onAdded).not.toHaveBeenCalled();
});

it("carries the over-limit choice into the invitation for a full group", async () => {
  vi.mocked(Groups.addMember).mockRejectedValue(noAccount());
  vi.mocked(Invitations.create).mockResolvedValue({ invitation: {} } as Awaited<ReturnType<typeof Invitations.create>>);
  panel({ memberCount: 3, memberLimit: 3 }); await openPanel(); await useEmail("new@example.com");
  await userEvent.click(screen.getByRole("button", { name: "Add member" }));
  await userEvent.click(await screen.findByRole("checkbox", { name: /Invite over the limit anyway/ }));
  await userEvent.click(screen.getByRole("button", { name: "Send invitation" }));
  expect(Invitations.create).toHaveBeenCalledExactlyOnceWith({ email: "new@example.com", groupId: 1, overLimit: true });
});

it("starts on the email field, with no name search, for someone who can't search", async () => {
  panel({ canSearch: false }); await openPanel();
  expect(screen.getByLabelText("Email address")).toBeInTheDocument();
  expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Search by name instead" })).not.toBeInTheDocument();
});

it("says a teen can only be asked by name when the email belongs to a teen", async () => {
  vi.mocked(Groups.addMember).mockRejectedValue(new ApiError(409, "teen", { code: "teen_add_approval_required" }));
  panel(); await openPanel(); await useEmail("teen@example.com");
  await userEvent.click(screen.getByRole("button", { name: "Add member" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Select this person by name, then choose Ask to add.");
});
