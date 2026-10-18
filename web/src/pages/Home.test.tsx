import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { GroupSummary } from "../types";
import { Home } from "./Home";

function group(id: number, name: string, tags: string[]): GroupSummary {
  return {
    id, name, description: "", tags, imageUrl: "", coverFocalX: 50, coverFocalY: 50,
    location: "", leaderName: "Grier", memberCount: 1, nextEventAt: null, status: "active",
    leaderId: 1, visibility: "public", deletionRequested: false, memberLimit: null,
  };
}

const all = [
  group(1, "Youth Choir", ["choir", "youth"]),
  group(2, "Sunday Choir", ["choir"]),
  group(3, "Youth Band", ["band", "youth"]),
  group(4, "Prayer Team", ["prayer"]),
];

// Same rules as the API: search narrows, and any selected tag matches (OR,
// case-insensitive).
const { list } = vi.hoisted(() => ({ list: vi.fn() }));
vi.mock("../api", async (importActual) => ({
  ApiError: (await importActual<typeof import("../api")>()).ApiError,
  Groups: { list },
  Events: { listOpen: () => Promise.resolve({ events: [] }) },
}));
function listLikeTheAPI(search = "", tags: string[] = []) {
  const want = tags.map((t) => t.toLowerCase());
  return Promise.resolve({
    groups: all.filter(
      (g) =>
        g.name.toLowerCase().includes(search.toLowerCase()) &&
        (want.length === 0 || g.tags.some((t) => want.includes(t.toLowerCase())))
    ),
  });
}

function renderHome(path = "/") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Home />
    </MemoryRouter>
  );
}

// The filter chips sit in the hero, apart from the tags on each card.
async function chips() {
  await screen.findByRole("button", { name: "All" });
  await screen.findAllByRole("button", { name: "prayer" });
  return within(screen.getByRole("button", { name: "All" }).parentElement!);
}

const summary = () => screen.getByText(/Showing groups tagged/);

describe("Home tag filters", () => {
  beforeEach(() => {
    list.mockReset();
    list.mockImplementation(listLikeTheAPI);
  });

  it("shows no summary or markers before a tag is chosen", async () => {
    renderHome();
    const bar = await chips();
    expect(screen.queryByText(/Showing groups tagged/)).not.toBeInTheDocument();
    expect(bar.getByRole("button", { name: "choir" })).toHaveAttribute("aria-pressed", "false");
    expect(bar.queryByText("+")).not.toBeInTheDocument();
    expect(bar.queryByText("✓")).not.toBeInTheDocument();
  });

  it("marks the selected tag, offers + on the others, and says what is shown", async () => {
    renderHome();
    const bar = await chips();
    await userEvent.click(bar.getByRole("button", { name: "choir" }));
    const choir = bar.getByRole("button", { name: "choir" });
    expect(choir).toHaveAttribute("aria-pressed", "true");
    // The check mark is drawn by .chip[aria-pressed="true"] in index.css.
    expect(within(choir).queryByText("+")).not.toBeInTheDocument();
    expect(within(bar.getByRole("button", { name: "youth" })).getByText("+")).toBeInTheDocument();
    expect(summary()).toHaveTextContent(/^Showing groups tagged choir\. Clear$/);
    expect(summary()).toHaveAttribute("aria-live", "polite");
    expect(within(summary()).getByText("choir").tagName).toBe("STRONG");
  });

  it("words two tags with or, and Clear resets the filter", async () => {
    renderHome();
    const bar = await chips();
    await userEvent.click(bar.getByRole("button", { name: "choir" }));
    await userEvent.click(bar.getByRole("button", { name: "band" }));
    expect(summary()).toHaveTextContent(/^Showing groups tagged choir or band\. Clear$/);
    expect(summary()).not.toHaveTextContent("must match");
    expect(await screen.findByRole("heading", { name: "Youth Band" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Sunday Choir" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Prayer Team" })).not.toBeInTheDocument();
    await userEvent.click(within(summary()).getByRole("button", { name: "Clear" }));
    expect(screen.queryByText(/Showing groups tagged/)).not.toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Prayer Team" })).toBeInTheDocument();
  });

  it("lists three or more tags with commas and a final or", async () => {
    renderHome("/?tag=choir&tag=youth&tag=band");
    await chips();
    expect(summary()).toHaveTextContent(/^Showing groups tagged choir, youth, or band\. Clear$/);
  });

  it("explains an empty search-plus-tags result and offers to clear the tags", async () => {
    renderHome("/?tag=choir&tag=band");
    await chips();
    await userEvent.type(screen.getByLabelText("Search groups"), "prayer");
    expect(
      await screen.findByText(/No groups match “prayer” with these tags/, undefined, { timeout: 2000 })
    ).toHaveTextContent("No groups match “prayer” with these tags. Try another search, or Clear the tags.");
    await userEvent.click(screen.getByRole("button", { name: "Clear the tags" }));
    expect(await screen.findByRole("heading", { name: "Prayer Team" })).toBeInTheDocument();
  });

  it("keeps the plain empty state when there is no search", async () => {
    list.mockImplementation((search = "", tags: string[] = []) =>
      tags.length ? Promise.resolve({ groups: [] }) : listLikeTheAPI(search, tags)
    );
    renderHome("/?tag=choir");
    expect(
      await screen.findByText("No groups found. Try a different search or start a new group.")
    ).toBeInTheDocument();
  });
});

// AUDIT: the home page once showed the server's raw "boom".
describe("Home load failure", () => {
  it("says what happened in plain words, never the server's text", async () => {
    const { ApiError } = await vi.importActual<typeof import("../api")>("../api");
    list.mockReset();
    list.mockRejectedValue(new ApiError(500, "boom", { error: "server error", code: "server_error" }));
    renderHome();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("We couldn't load the groups. Please try again in a minute.");
    expect(alert).not.toHaveTextContent("boom");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});
