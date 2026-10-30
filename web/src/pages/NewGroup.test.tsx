import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, RouterProvider, Routes, createMemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { Groups } from "../api";
import type { GroupSummary } from "../types";
import { NewGroup } from "./NewGroup";

const auth = vi.hoisted(() => ({ isAdmin: false }));
vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 2 }, loading: false, isAdmin: auth.isAdmin }) }));
vi.mock("../api", async (actual) => ({ ...(await actual<typeof import("../api")>()), Groups: { create: vi.fn(), update: vi.fn() } }));

const made = (status: string): { group: GroupSummary } => ({ group: { id: 9, name: "Sam's study", status } as GroupSummary });
function open() {
  render(<MemoryRouter initialEntries={["/groups/new"]}><Routes>
    <Route path="/groups/new" element={<NewGroup />} />
    <Route path="/" element={<h1>Home page</h1>} />
  </Routes></MemoryRouter>);
}
async function submit(name = "Sam's study") {
  await userEvent.type(screen.getByLabelText("Group name"), name);
  await userEvent.click(screen.getByRole("button", { name: "Create group" }));
}
beforeEach(() => { vi.resetAllMocks(); auth.isAdmin = false; });

it("tells a non-admin that an admin reviews new groups first", () => {
  open();
  expect(screen.getByText("An admin reviews new groups before they go live.")).toBeInTheDocument();
});

it("leaves the review line off for an admin", () => {
  auth.isAdmin = true;
  open();
  expect(screen.queryByText(/reviews new groups/)).not.toBeInTheDocument();
});

it("keeps name, description, location and visibility visible, and tucks the rest under More options", async () => {
  open();
  for (const label of ["Group name", "Description (optional)", "Location (optional)"]) expect(screen.getByLabelText(label)).toBeVisible();
  expect(screen.getByRole("radio", { name: /^Public/ })).toBeVisible();
  const more = screen.getByRole("button", { name: "More options" });
  expect(more).toHaveAttribute("aria-expanded", "false");
  expect(screen.getByLabelText(/Tags/)).not.toBeVisible();
  expect(screen.getByLabelText(/Member limit/)).not.toBeVisible();
  expect(screen.queryByRole("button", { name: "Choose photo" })).not.toBeInTheDocument();
  await userEvent.click(more);
  expect(more).toHaveAttribute("aria-expanded", "true");
  expect(screen.getByLabelText(/Tags/)).toBeVisible();
  expect(screen.getByLabelText(/Member limit/)).toBeVisible();
  // No photo yet: the pattern thumbnail stands in (the one coverSrc gives id 0) beside "Choose photo".
  expect(screen.getByRole("button", { name: "Choose photo" })).toBeVisible();
});

it("opens More options and focuses the member limit when it is invalid", async () => {
  open();
  await userEvent.click(screen.getByRole("button", { name: "More options" }));
  await userEvent.type(screen.getByLabelText(/Member limit/), "0");
  await userEvent.click(screen.getByRole("button", { name: "More options" }));
  expect(screen.getByLabelText(/Member limit/)).not.toBeVisible();
  await userEvent.type(screen.getByLabelText("Group name"), "Study");
  await userEvent.click(screen.getByRole("button", { name: "Create group" }));
  expect(screen.getByRole("button", { name: "More options" })).toHaveAttribute("aria-expanded", "true");
  expect(screen.getByLabelText(/Member limit/)).toHaveFocus();
  expect(Groups.create).not.toHaveBeenCalled();
});

it("sends tags and member limit entered under More options", async () => {
  vi.mocked(Groups.create).mockResolvedValue(made("pending"));
  open();
  await userEvent.click(screen.getByRole("button", { name: "More options" }));
  await userEvent.type(screen.getByLabelText(/Tags/), "young-adults, bible-study");
  await userEvent.type(screen.getByLabelText(/Member limit/), "12");
  await submit();
  expect(Groups.create).toHaveBeenCalledWith(expect.objectContaining({ name: "Sam's study", tags: ["young-adults", "bible-study"], memberLimit: 12 }));
});

it("shows a calm thank-you, not a warning, once a group is waiting for approval", async () => {
  vi.mocked(Groups.create).mockResolvedValue(made("pending"));
  open(); await submit();
  expect(await screen.findByRole("heading", { level: 1, name: "Thanks! An admin will review “Sam's study”." })).toBeInTheDocument();
  expect(screen.getByText("We'll let you know in Alerts when it's approved.")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Back to Home" })).toHaveAttribute("href", "/");
  expect(screen.queryByText("Pending approval")).not.toBeInTheDocument();
  expect(screen.queryByText("!")).not.toBeInTheDocument();
});

it("offers an admin the first meeting once the group is live", async () => {
  auth.isAdmin = true;
  vi.mocked(Groups.create).mockResolvedValue(made("active"));
  open(); await submit();
  expect(await screen.findByRole("heading", { level: 1, name: "Group created" })).toBeInTheDocument();
  expect(screen.getByText("“Sam's study” is live.")).toBeInTheDocument();
  const first = screen.getByRole("link", { name: "Schedule the first meeting" });
  expect(first).toHaveAttribute("href", "/groups/9/events/new");
  expect(first).toHaveClass("btn-primary");
  const group = screen.getByRole("link", { name: "Go to the group" });
  expect(group).toHaveAttribute("href", "/groups/9");
  expect(group).toHaveClass("btn-secondary");
});

it("keeps the form and its edits when creating fails", async () => {
  vi.mocked(Groups.create).mockRejectedValue(new Error("offline"));
  open(); await submit();
  await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument());
  expect(screen.getByLabelText("Group name")).toHaveValue("Sam's study");
});

it("asks before Cancel throws away a half-filled new group, but not an empty form", async () => {
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  const router = createMemoryRouter([
    { path: "/groups/new", element: <NewGroup /> },
    { path: "/", element: <h1>Home page</h1> },
  ], { initialEntries: ["/groups/new"] });
  render(<RouterProvider router={router} />);
  await userEvent.type(screen.getByLabelText("Group name"), "Half");
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(confirm).toHaveBeenCalledWith("Leave without saving? Your changes will be lost.");
  expect(screen.queryByRole("heading", { name: "Home page" })).not.toBeInTheDocument();
  confirm.mockReturnValue(true);
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(await screen.findByRole("heading", { name: "Home page" })).toBeInTheDocument();
});

it("lets a created group's screen go on without the leave prompt", async () => {
  vi.mocked(Groups.create).mockResolvedValue(made("pending"));
  const confirm = vi.spyOn(window, "confirm");
  const router = createMemoryRouter([
    { path: "/groups/new", element: <NewGroup /> },
    { path: "/", element: <h1>Home page</h1> },
  ], { initialEntries: ["/groups/new"] });
  render(<RouterProvider router={router} />);
  await submit();
  await userEvent.click(await screen.findByRole("link", { name: "Back to Home" }));
  expect(await screen.findByRole("heading", { name: "Home page" })).toBeInTheDocument();
  expect(confirm).not.toHaveBeenCalled();
});
