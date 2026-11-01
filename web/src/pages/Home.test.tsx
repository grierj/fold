import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CalendarOccurrence, GroupSummary } from "../types";
import { Home } from "./Home";

function group(id: number, name: string, tags: string[]): GroupSummary {
  return {
    id, name, description: "", tags, imageUrl: "", coverFocalX: 50, coverFocalY: 50,
    location: "", leaderName: "Grier", memberCount: 1, nextEventAt: null, status: "active",
    leaderId: 1, visibility: "public", deletionRequested: false, memberLimit: null,
  } as GroupSummary;
}

const all = [
  group(1, "Youth Choir", ["choir", "youth"]),
  group(2, "Sunday Choir", ["choir"]),
  group(3, "Youth Band", ["band", "youth"]),
  group(4, "Prayer Team", ["prayer"]),
];

// Same rules as the API: search narrows, and any selected tag matches (OR,
// case-insensitive).
const { list, auth, site, calendar, openEvents } = vi.hoisted(() => ({
  list: vi.fn(), auth: vi.fn(), site: vi.fn(), calendar: vi.fn(), openEvents: vi.fn(),
}));
vi.mock("../auth", () => ({ useAuth: auth }));
vi.mock("../api", async (importActual) => ({
  ApiError: (await importActual<typeof import("../api")>()).ApiError,
  Groups: { list },
  Events: { listOpen: openEvents },
  Site: { get: site },
  Calendar: { list: calendar },
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

beforeEach(() => {
  site.mockReset();
  site.mockResolvedValue({ name: "Fold", timezone: "UTC", selfSignup: "request" });
  calendar.mockReset();
  calendar.mockResolvedValue({ occurrences: [], truncated: false });
  openEvents.mockReset();
  openEvents.mockResolvedValue({ events: [] });
  list.mockImplementation(listLikeTheAPI);
});

describe("Home tag filters", () => {
  beforeEach(() => {
    auth.mockReturnValue({ user: null, loading: false });
    list.mockReset();
    list.mockImplementation(listLikeTheAPI);
  });

  it.each([false, true])("prioritizes only the first visible group cover, including after filtering (signed in=%s)", async (signedIn) => {
    auth.mockReturnValue({ user: signedIn ? { id: 2, displayName: "Sam" } : null, loading: false });
    const { container } = renderHome();
    await screen.findByRole("heading", { name: "Youth Choir" });
    const images = container.querySelectorAll("#groups a img");
    expect(images).toHaveLength(4);
    images.forEach((image, index) => {
      expect(image).toHaveAttribute("loading", index === 0 ? "eager" : "lazy");
      expect(image).toHaveAttribute("fetchpriority", index === 0 ? "high" : "auto");
    });
    await userEvent.click((await chips()).getByRole("button", { name: "prayer" }));
    await waitFor(() => expect(container.querySelectorAll("#groups a img")).toHaveLength(1));
    const first = screen.getByRole("link", { name: /Prayer Team/ }).querySelector("img");
    expect(first).toHaveAttribute("loading", "eager");
    expect(first).toHaveAttribute("fetchpriority", "high");
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
      await screen.findByText(/None of them have these tags/, undefined, { timeout: 2000 })
    ).toHaveTextContent("None of them have these tags. Try another search, or Clear the tags.");
    expect(screen.getByText("No groups match “prayer”")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Clear the tags" }));
    expect(await screen.findByRole("heading", { name: "Prayer Team" })).toBeInTheDocument();
  });

  it("keeps the plain empty state when there is no search", async () => {
    list.mockImplementation((search = "", tags: string[] = []) =>
      tags.length ? Promise.resolve({ groups: [] }) : listLikeTheAPI(search, tags)
    );
    renderHome("/?tag=choir");
    expect(await screen.findByText("No groups match these tags")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Clear the tags" })).toBeInTheDocument();
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

describe("Home signed out (DEC-94)", () => {
  beforeEach(() => auth.mockReturnValue({ user: null, loading: false }));

  it("keeps the headline, names the church and offers both buttons", async () => {
    site.mockResolvedValue({ name: "Grace Church", timezone: "UTC", selfSignup: "request" });
    renderHome();
    expect(await screen.findByText("Welcome to Grace Church")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Find your people. Grow in faith together.");
    expect(screen.getByRole("link", { name: "Request an account" })).toHaveAttribute("href", "/join");
    expect(screen.getByRole("link", { name: "Browse public events" })).toHaveAttribute("href", "/calendar");
    expect(calendar).not.toHaveBeenCalled();
  });

  it("omits the eyebrow for the default name", async () => {
    renderHome();
    await screen.findByRole("link", { name: "Request an account" });
    expect(screen.queryByText(/Welcome to/)).not.toBeInTheDocument();
  });

  it("drops Request an account when self-signup is off", async () => {
    site.mockResolvedValue({ name: "Grace Church", timezone: "UTC", selfSignup: "off" });
    renderHome();
    expect(await screen.findByRole("link", { name: "Browse public events" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Request an account" })).not.toBeInTheDocument();
  });

  it("shows no Log in button in the hero when self-signup is off (the header has it)", async () => {
    site.mockResolvedValue({ name: "Grace Church", timezone: "UTC", selfSignup: "off" });
    renderHome();
    expect(await screen.findByRole("link", { name: "Browse public events" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Log in" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Request an account" })).not.toBeInTheDocument();
  });

  it("offers no buttons until auth resolves", async () => {
    auth.mockReturnValue({ user: null, loading: true });
    renderHome();
    await chips();
    expect(screen.queryByRole("link", { name: "Browse public events" })).not.toBeInTheDocument();
  });

  it("lists church events", async () => {
    openEvents.mockResolvedValue({ events: [{ eventId: 9, date: "2030-01-01", startsAt: "2030-01-01T18:00:00Z", title: "Potluck", location: "Hall", description: "" }] });
    renderHome();
    expect(await screen.findByRole("heading", { name: "Church events" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Potluck/ })).toHaveAttribute("href", "/events/9");
    expect(screen.queryByRole("heading", { name: /Coming up/ })).not.toBeInTheDocument();
  });
});

function occ(n: number, extra: Partial<CalendarOccurrence> = {}): CalendarOccurrence {
  const day = String(10 + n).padStart(2, "0");
  return {
    eventId: n, date: `2030-01-${day}`, startsAt: `2030-01-${day}T19:00:00Z`, endsAt: `2030-01-${day}T20:00:00Z`,
    title: `Event ${n}`, location: "", groupId: 1, groupName: "Youth Choir", churchWide: false,
    remoteAvailable: false, childcareAvailable: false, myStatus: null, ownScope: true, ...extra,
  };
}

describe("Home signed in (DEC-94)", () => {
  beforeEach(() => {
    auth.mockReturnValue({ user: { id: 1, name: "Samuel Adams", displayName: "Sam" }, loading: false });
    // Fake only the clock, so the component's own timers and promises still run.
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date(2030, 0, 10, 9, 0, 0));
  });
  afterEach(() => vi.useRealTimers());

  async function ready() {
    await screen.findByRole("heading", { name: "Groups" });
    await screen.findAllByRole("heading", { name: "Youth Choir" });
  }

  it.each([
    [0, "Good morning"], [11, "Good morning"], [12, "Good afternoon"], [16, "Good afternoon"], [17, "Good evening"], [23, "Good evening"],
  ])("greets at hour %i with %s", async (hour, hello) => {
    vi.setSystemTime(new Date(2030, 0, 10, hour, 30, 0));
    renderHome();
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent(`${hello}, Sam`);
    await ready();
  });

  it("falls back to the first name, then to Welcome back, never the email", async () => {
    auth.mockReturnValue({ user: { id: 1, name: "Samuel Adams", displayName: "", email: "s@example.com" }, loading: false });
    const { unmount } = renderHome();
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Good morning, Samuel");
    expect(screen.getByRole("heading", { level: 1 })).not.toHaveTextContent("Adams");
    await ready();
    unmount();
    auth.mockReturnValue({ user: { id: 1, name: "", displayName: "", email: "s@example.com" }, loading: false });
    renderHome();
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Welcome back");
    expect(screen.getByRole("heading", { level: 1 })).not.toHaveTextContent("example.com");
    await ready();
  });

  it("has no hero, buttons, eyebrow or church events block", async () => {
    openEvents.mockResolvedValue({ events: [{ eventId: 9, date: "2030-01-01", startsAt: "2030-01-01T18:00:00Z", title: "Potluck", location: "", description: "" }] });
    renderHome();
    await ready();
    expect(screen.queryByText("Find your people. Grow in faith together.")).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Browse public events" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Request an account" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Church events" })).not.toBeInTheDocument();
    expect(openEvents).not.toHaveBeenCalled();
    expect(site).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Search groups")).toBeInTheDocument();
  });

  it("asks Just mine for the next 60 days and shows rows with answers and links", async () => {
    calendar.mockResolvedValue({
      truncated: false,
      occurrences: [
        occ(2, { myStatus: "maybe", location: "Room 4" }),
        occ(1, { myStatus: "going", dateUrl: "/events/1/dates/2030-01-11" }),
        occ(3, { churchWide: true, groupName: undefined, groupId: undefined, myStatus: "waitlisted" }),
        occ(4, { ownScope: true, myStatus: null }),
        occ(5),
      ],
    });
    renderHome();
    const section = (await screen.findByRole("heading", { name: "Coming up for you" })).closest("section")!;
    expect(calendar).toHaveBeenCalledWith(expect.objectContaining({ from: "2030-01-10", to: "2030-03-11", mine: true }));
    expect(within(section).getByRole("link", { name: "See calendar" })).toHaveAttribute("href", "/calendar?mine=1");
    const rows = await within(section).findAllByRole("listitem");
    expect(rows).toHaveLength(4);
    expect(rows[0]).toHaveTextContent("Event 1");
    expect(rows[0]).toHaveTextContent("Going");
    // The same badge the calendar rows use: an icon for Going, the label as its name.
    const badge = within(rows[0]).getByLabelText("Going");
    expect(badge).toHaveClass("inline-flex", "text-success-on-subtle");
    expect(badge.querySelector("svg")).not.toBeNull();
    expect(within(rows[0]).getByRole("link")).toHaveAttribute("href", "/events/1/dates/2030-01-11");
    expect(rows[1]).toHaveTextContent("Maybe");
    expect(rows[1]).toHaveTextContent("Youth Choir · Room 4");
    expect(within(rows[1]).getByRole("link")).toHaveAttribute("href", "/events/2");
    expect(rows[2]).toHaveTextContent("Church-wide");
    expect(rows[2]).toHaveTextContent("Waitlisted");
    expect(rows[3]).toHaveTextContent("Not answered");
    expect(within(section).queryByText("Event 5")).not.toBeInTheDocument();
    await ready();
  });

  it("skips dates that already ended or that the person can't attend", async () => {
    calendar.mockResolvedValue({
      truncated: false,
      occurrences: [
        occ(0, { startsAt: "2030-01-10T05:00:00Z", endsAt: "2030-01-10T06:00:00Z", title: "Earlier today" }),
        occ(1, { myStatus: "declined", title: "Can't make it" }),
        occ(2, { myStatus: "going", title: "Still on" }),
      ],
    });
    renderHome();
    expect(await screen.findByText("Still on")).toBeInTheDocument();
    expect(screen.queryByText("Earlier today")).not.toBeInTheDocument();
    expect(screen.queryByText("Can't make it")).not.toBeInTheDocument();
    await ready();
  });

  it("has a short empty state", async () => {
    renderHome();
    expect(await screen.findByText("Nothing coming up in the next two months")).toBeInTheDocument();
    expect(screen.getByText("Dates from your groups, and events you answer, show up here.")).toBeInTheDocument();
    await ready();
  });

  it("shows a loading state in place", async () => {
    calendar.mockReturnValue(new Promise(() => {}));
    renderHome();
    expect(await screen.findByText("Loading your dates…")).toBeInTheDocument();
    await ready();
  });

  it("keeps a calendar failure to its own section, and retries", async () => {
    const { ApiError } = await vi.importActual<typeof import("../api")>("../api");
    calendar.mockRejectedValueOnce(new ApiError(500, "boom", { error: "server error", code: "server_error" }));
    renderHome();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("We couldn't load your calendar. Please try again in a minute.");
    expect(alert).not.toHaveTextContent("boom");
    await ready();
    expect(screen.getAllByRole("alert")).toHaveLength(1);
    calendar.mockResolvedValue({ occurrences: [occ(1, { myStatus: "going" })], truncated: false });
    await userEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Event 1")).toBeInTheDocument();
  });

  it("wraps the groups list in #groups and scrolls to it for /#groups", async () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    renderHome("/#groups");
    await ready();
    const target = screen.getByRole("heading", { name: "Groups" }).closest("section")!;
    expect(target).toHaveAttribute("id", "groups");
    expect(target).toContainElement(screen.getAllByRole("heading", { name: "Youth Choir" })[0]);
    expect(scroll).toHaveBeenCalled();
    expect(scroll.mock.contexts[0]).toBe(target);
  });
});
