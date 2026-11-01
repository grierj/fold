import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import type { GroupSummary } from "../types";
import { Groups } from "../api";
import { DirectoryPositionProvider } from "../lib/directoryPosition";
import { GroupDirectory } from "./GroupDirectory";

vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 1 } }) }));
vi.mock("../api", () => ({ Groups: { list: vi.fn() } }));
const groups: GroupSummary[] = Array.from({ length: 50 }, (_, i) => ({
  id: i + 1, name: `Group ${String(i + 1).padStart(2, "0")}`, description: "", tags: [i % 2 ? "music" : "study"],
  imageUrl: "", coverFocalX: 50, coverFocalY: 50, location: "Hall", leaderName: "Lee", memberCount: 10,
  nextEventAt: null, status: "active", leaderId: 1, visibility: "public", deletionRequested: false, memberLimit: null,
}));
const jumped = vi.fn();
function Location() { const loc = useLocation(); return <span data-testid="url">{loc.pathname}{loc.search}</span>; }
function Detail() { const go = useNavigate(); return <button onClick={() => go(-1)}>Back to results</button>; }
function view(path = "/", ready = true) {
  return render(<MemoryRouter initialEntries={[path]}><DirectoryPositionProvider><Location /><Routes>
    <Route path="/" element={<GroupDirectory ready={ready} jump={false} onJumped={jumped} />} />
    <Route path="/groups/:id" element={<Detail />} />
  </Routes></DirectoryPositionProvider></MemoryRouter>);
}
beforeEach(() => {
  vi.resetAllMocks();
  window.scrollTo = vi.fn();
  Element.prototype.scrollIntoView = vi.fn();
  vi.mocked(Groups.list).mockImplementation(async (search = "", tags = []) => ({ groups: groups.filter((g) => g.name.includes(search) && (!tags.length || tags.some((t) => g.tags.includes(t)))) }));
});

it("starts with twelve, appends in order and announces the exact total", async () => {
  view();
  await screen.findByText("Showing 12 of 50 groups");
  expect(screen.getAllByRole("heading", { level: 3 })).toHaveLength(12);
  expect(Groups.list).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("heading", { name: "Group 13" })).not.toBeInTheDocument();
  const more = screen.getByRole("button", { name: "Show more groups" });
  more.focus();
  await userEvent.keyboard("{Enter}");
  expect(await screen.findByText("Showing 24 of 50 groups")).toBeInTheDocument();
  expect(more).toHaveFocus();
  expect(screen.getByTestId("url")).toHaveTextContent("shown=24");
  await userEvent.tab();
  expect(screen.getByRole("link", { name: /Group 13/ })).toHaveFocus();
  for (let i = 0; i < 3; i++) await userEvent.click(screen.getByRole("button", { name: "Show more groups" }));
  expect(screen.getAllByRole("heading", { level: 3 })).toHaveLength(50);
  expect(screen.getByRole("status")).toHaveTextContent("50 groups");
  expect(screen.queryByRole("button", { name: "Show more groups" })).not.toBeInTheDocument();
  expect(screen.getByRole("link", { name: /Group 49/ })).toHaveFocus();
});

it("keeps search and loaded results through detail and Back", async () => {
  view("/?search=Group&shown=24");
  await screen.findByRole("heading", { name: "Group 24" });
  await userEvent.click(screen.getByRole("link", { name: /Group 20/ }));
  await userEvent.click(screen.getByRole("button", { name: "Back to results" }));
  await screen.findByRole("heading", { name: "Group 24" });
  expect(screen.getByRole("searchbox")).toHaveValue("Group");
  expect(screen.getByTestId("url")).toHaveTextContent("?search=Group&shown=24");
  await waitFor(() => expect(screen.getByRole("link", { name: /Group 20/ })).toHaveFocus());
});

it("resets the batch for a new search, retaining other URL parameters", async () => {
  view("/?shown=36&source=shared");
  await screen.findByRole("heading", { name: "Group 36" });
  await userEvent.type(screen.getByRole("searchbox"), "Group 50");
  await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent(/^1 group$/));
  expect(screen.getByTestId("url")).toHaveTextContent("source=shared&search=Group+50");
  expect(screen.getByTestId("url")).not.toHaveTextContent("shown=");
});

it("collapses available tags but keeps selected tags removable", async () => {
  view("/?tag=music&shown=36");
  await screen.findByText("25 groups");
  expect(screen.queryByRole("button", { name: "study" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Filters (1)" }));
  const panel = screen.getByRole("group", { name: "Group tags" });
  await userEvent.click(within(panel).getByRole("button", { name: "study" }));
  await screen.findByText("Showing 12 of 50 groups");
  await userEvent.click(screen.getByRole("button", { name: "Done with filters" }));
  expect(screen.getByRole("searchbox")).toHaveFocus();
  expect(screen.getByRole("button", { name: "Filters (2)" })).toHaveAttribute("aria-expanded", "false");
  await userEvent.click(screen.getByRole("button", { name: "Remove music filter" }));
  await screen.findByText("Showing 12 of 25 groups");
});

it.each(["-1", "0", "NaN", "Infinity"])("treats invalid shown=%s as the first batch", async (shown) => {
  view(`/?shown=${shown}`);
  await screen.findByText("Showing 12 of 50 groups");
});

it("waits for content above the directory before following the anchor", async () => {
  const ui = (ready: boolean) => <MemoryRouter initialEntries={["/#groups"]}><GroupDirectory ready={ready} jump={false} onJumped={jumped} /></MemoryRouter>;
  const rendered = render(ui(false));
  await screen.findByText("Showing 12 of 50 groups");
  expect(Element.prototype.scrollIntoView).not.toHaveBeenCalled();
  rendered.rerender(ui(true));
  await waitFor(() => expect(Element.prototype.scrollIntoView).toHaveBeenCalledOnce());
});

it("ignores a stale response after the query changes", async () => {
  let resolve!: (value: { groups: GroupSummary[] }) => void;
  vi.mocked(Groups.list).mockImplementation((query = "") => query ? Promise.resolve({ groups: [groups[49]] }) : new Promise((r) => { resolve = r; }));
  view();
  await userEvent.type(screen.getByRole("searchbox"), "Group 50");
  await screen.findByRole("heading", { name: "Group 50" });
  await act(async () => resolve({ groups }));
  expect(screen.getAllByRole("heading", { level: 3 })).toHaveLength(1);
  await userEvent.click(screen.getByRole("button", { name: "Filters" }));
  const choices = screen.getByRole("group", { name: "Group tags" });
  expect(within(choices).getByRole("button", { name: "study" })).toBeVisible();
  expect(within(choices).getByRole("button", { name: "music" })).toBeVisible();
});

it("brings the filter disclosure into view when opening it from the sticky toolbar", async () => {
  view();
  await screen.findByText("Showing 12 of 50 groups");
  expect(Element.prototype.scrollIntoView).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Filters" }));
  expect(Element.prototype.scrollIntoView).toHaveBeenCalledWith({ block: "start" });
  expect(screen.getByRole("button", { name: "Filters" })).toHaveFocus();
});

it("preserves keyboard focus when selected tags or Clear disappear", async () => {
  view("/?tag=music&tag=study");
  await screen.findByText("Showing 12 of 50 groups");
  screen.getByRole("button", { name: "Remove music filter" }).focus();
  await userEvent.keyboard("{Enter}");
  expect(screen.getByRole("button", { name: "Filters (1)" })).toHaveFocus();
  screen.getByRole("button", { name: "Clear" }).focus();
  await userEvent.keyboard("{Enter}");
  expect(screen.getByRole("button", { name: "Filters" })).toHaveFocus();
  expect(screen.queryByRole("group", { name: "Selected group filters" })).not.toBeInTheDocument();
});
