import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { Groups } from "../api";
import type { GroupSummary } from "../types";
import { MyGroups } from "./MyGroups";

vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 2 }, loading: false, isAdmin: false }) }));
vi.mock("../api", async (actual) => ({ ...(await actual<typeof import("../api")>()), Groups: { mine: vi.fn(), list: vi.fn() } }));

function group(id: number, over: Partial<GroupSummary> = {}): GroupSummary {
  return {
    id, name: `Group ${id}`, description: "", tags: [], imageUrl: "", coverFocalX: 50, coverFocalY: 50,
    location: "", leaderName: "Lee", memberCount: 3, nextEventAt: "2035-06-16T01:00:00Z", status: "active",
    leaderId: 1, visibility: "public", deletionRequested: false, memberLimit: null, ...over,
  };
}
function open() {
  render(<MemoryRouter><MyGroups /></MemoryRouter>);
}
beforeEach(() => {
  vi.resetAllMocks();
});

it("labels only the groups you lead", async () => {
  vi.mocked(Groups.mine).mockResolvedValue({ groups: [group(1, { myRole: "leader" }), group(2, { myRole: "member" })] });
  open();
  expect(await screen.findByRole("heading", { name: "Group 1" })).toBeInTheDocument();
  expect(screen.getAllByText("You lead")).toHaveLength(1);
  expect(screen.queryByText("You're in")).not.toBeInTheDocument();
  expect(Groups.list).not.toHaveBeenCalled();
});

it("sends a person in no groups to the directory or the start form, with groups meeting soon", async () => {
  vi.mocked(Groups.mine).mockResolvedValue({ groups: [] });
  vi.mocked(Groups.list).mockResolvedValue({
    groups: [
      group(10, { myRole: "member" }),
      group(11, { nextEventAt: null }),
      group(12), group(13), group(14), group(15),
    ],
  });
  open();
  expect(await screen.findByText("You're not in any groups yet")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Browse groups" })).toHaveAttribute("href", "/#groups");
  expect(screen.getByText(/^or/)).toBeInTheDocument();
  const start = screen.getByRole("link", { name: "start a group" });
  expect(start).toHaveAttribute("href", "/groups/new");
  expect(start).toHaveClass("text-link");
  expect(start).not.toHaveClass("btn-link");
  expect(screen.queryByRole("link", { name: "Start a group" })).not.toBeInTheDocument();

  expect(await screen.findByRole("heading", { name: "Groups meeting soon" })).toBeInTheDocument();
  const names = screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent);
  expect(names).toEqual(["Group 12", "Group 13", "Group 14"]);
});

it("hides the meeting-soon section when nothing qualifies or the directory fails", async () => {
  vi.mocked(Groups.mine).mockResolvedValue({ groups: [] });
  vi.mocked(Groups.list).mockResolvedValue({ groups: [group(10, { myRole: "member" }), group(11, { nextEventAt: null })] });
  const { unmount } = render(<MemoryRouter><MyGroups /></MemoryRouter>);
  await waitFor(() => expect(Groups.list).toHaveBeenCalled());
  await waitFor(() => expect(screen.queryByRole("heading", { name: "Groups meeting soon" })).not.toBeInTheDocument());
  unmount();

  vi.mocked(Groups.list).mockRejectedValue(new Error("offline"));
  open();
  await waitFor(() => expect(Groups.list).toHaveBeenCalledTimes(2));
  await waitFor(() => expect(screen.queryByRole("heading", { name: "Groups meeting soon" })).not.toBeInTheDocument());
  expect(screen.getByText("You're not in any groups yet")).toBeInTheDocument();
});

it("shows nothing for meeting soon while the directory loads, then the section once a group qualifies", async () => {
  vi.mocked(Groups.mine).mockResolvedValue({ groups: [] });
  let resolve!: (v: { groups: GroupSummary[] }) => void;
  vi.mocked(Groups.list).mockReturnValue(new Promise((r) => { resolve = r; }));
  open();
  expect(await screen.findByText("You're not in any groups yet")).toBeInTheDocument();
  await waitFor(() => expect(Groups.list).toHaveBeenCalled());
  expect(screen.queryByRole("heading", { name: "Groups meeting soon" })).not.toBeInTheDocument();
  expect(screen.queryByText("Loading groups meeting soon…")).not.toBeInTheDocument();
  resolve({ groups: [group(12)] });
  expect(await screen.findByRole("heading", { name: "Groups meeting soon" })).toBeInTheDocument();
});
