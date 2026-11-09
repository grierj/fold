import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { Layout } from "./Layout";

const { auth } = vi.hoisted(() => ({ auth: vi.fn() }));
vi.mock("../auth", () => ({ useAuth: auth }));
vi.mock("../api/site", () => ({ Site: { get: () => Promise.resolve({ name: "Grace Community Church" }) } }));
vi.mock("../lib/notifications", () => ({ useBadge: () => ({ unread: 0, waiting: 0, loaded: false }) }));
vi.mock("./Notifications", () => ({ NotificationBell: () => null, NotificationTab: () => null }));
beforeEach(() => auth.mockReturnValue({ user: null, loading: false, logout: vi.fn(), isAdmin: false }));

it("names the church, offers the skip link and main region, and shows signed-out controls", async () => {
  render(<MemoryRouter><Layout><h1>Welcome</h1></Layout></MemoryRouter>);
  expect(await screen.findByText("Grace Community Church")).toBeInTheDocument();
  expect(screen.getAllByRole("link")[0]).toHaveTextContent("Skip to main content");
  expect(screen.getByRole("link", { name: "Skip to main content" })).toHaveAttribute("href", "#main");
  expect(screen.getByRole("main")).toHaveAttribute("id", "main");
  expect(screen.getByRole("main")).toHaveAttribute("tabindex", "-1");
  expect(screen.getAllByText(/powered by Fold/i)).toHaveLength(1);
  const nav = screen.getByRole("navigation", { name: "Main" });
  expect(within(nav).getByRole("link", { name: "Log in" })).toBeInTheDocument();
  expect(within(nav).getByRole("link", { name: "Sign up" })).toBeInTheDocument();
  expect(within(nav).getByRole("link", { name: "Calendar" })).toBeInTheDocument();
});

it("turns the header's Log in into a plain link on the sign-in pages", async () => {
  render(<MemoryRouter initialEntries={["/login"]}><Layout><h1>Welcome</h1></Layout></MemoryRouter>);
  await screen.findByText("Grace Community Church");
  const login = within(screen.getByRole("navigation", { name: "Main" })).getByRole("link", { name: "Log in" });
  expect(login).not.toHaveClass("btn-primary");
  expect(login).not.toHaveClass("btn-secondary");
});

it("keeps Log in a solid button elsewhere", async () => {
  render(<MemoryRouter initialEntries={["/calendar"]}><Layout><h1>Welcome</h1></Layout></MemoryRouter>);
  await screen.findByText("Grace Community Church");
  expect(within(screen.getByRole("navigation", { name: "Main" })).getByRole("link", { name: "Log in" })).toHaveClass("btn-primary");
});

it("keeps scroll offsets in sync with a wrapping header and cleans up", async () => {
  let height = 83;
  let resize!: () => void;
  const disconnect = vi.fn();
  const bounds = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(() => new DOMRect(0, 0, 768, height));
  vi.stubGlobal("ResizeObserver", class {
    constructor(callback: () => void) { resize = callback; }
    observe() {}
    disconnect = disconnect;
  });
  try {
    const view = render(<MemoryRouter><Layout><h1>Welcome</h1></Layout></MemoryRouter>);
    await screen.findByText("Grace Community Church");
    expect(document.documentElement.style.getPropertyValue("--fold-header-height")).toBe("83px");
    height = 100;
    act(() => resize());
    expect(document.documentElement.style.getPropertyValue("--fold-header-height")).toBe("100px");
    view.unmount();
    expect(disconnect).toHaveBeenCalledOnce();
    expect(document.documentElement.style.getPropertyValue("--fold-header-height")).toBe("");
  } finally {
    bounds.mockRestore();
    vi.unstubAllGlobals();
  }
});

it("moves keyboard focus into main when the skip link is activated", async () => {
  render(<MemoryRouter><Layout><h1>Welcome</h1></Layout></MemoryRouter>);
  const main = screen.getByRole("main");
  main.scrollIntoView = vi.fn();
  await userEvent.tab();
  expect(screen.getByRole("link", { name: "Skip to main content" })).toHaveFocus();
  await userEvent.keyboard("{Enter}");
  expect(main).toHaveFocus();
  expect(main.scrollIntoView).toHaveBeenCalledWith({ block: "start" });
});

it("offers New group as a labelled link, not a bare icon", async () => {
  auth.mockReturnValue({ user: { id: 1, name: "Sam", displayName: "Sam" }, loading: false, logout: vi.fn(), isAdmin: false });
  render(<MemoryRouter><Layout><h1>Welcome</h1></Layout></MemoryRouter>);
  await screen.findByText("Grace Community Church");
  const actions = screen.getAllByRole("link", { name: "New group" });
  for (const action of actions) expect(action).toHaveTextContent("New group");
});

it("shows Messages in the top nav only to people who can compose (M3.4c)", async () => {
  const user = { id: 2, name: "Lee", displayName: "", avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50 };
  auth.mockReturnValue({ user, loading: false, logout: vi.fn(), isAdmin: false, canCompose: false });
  const { unmount } = render(<MemoryRouter><Layout><h1>Welcome</h1></Layout></MemoryRouter>);
  await screen.findByText("Grace Community Church");
  expect(screen.queryByRole("link", { name: "Messages" })).not.toBeInTheDocument();
  unmount();
  auth.mockReturnValue({ user, loading: false, logout: vi.fn(), isAdmin: false, canCompose: true });
  const { unmount: unmount2 } = render(<MemoryRouter><Layout><h1>Welcome</h1></Layout></MemoryRouter>);
  await screen.findByText("Grace Community Church");
  const link = within(screen.getByRole("navigation", { name: "Main" })).getByRole("link", { name: "Messages" });
  expect(link).toHaveAttribute("href", "/messages");
  unmount2();
  auth.mockReturnValue({ user, loading: false, logout: vi.fn(), isAdmin: true, canCompose: true });
  render(<MemoryRouter><Layout><h1>Welcome</h1></Layout></MemoryRouter>);
  await screen.findByText("Grace Community Church");
  expect(within(screen.getByRole("navigation", { name: "Main" })).getByRole("link", { name: "Messages" })).toHaveAttribute("href", "/messages");
});
