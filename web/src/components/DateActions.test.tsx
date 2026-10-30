import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DateActions } from "./DateActions";

// The polite live region that says "Link copied" (not role=status; see DateActions).
const note = () => document.querySelector<HTMLElement>("[aria-live=\"polite\"]")!;

function renderActions(sharePath = "/events/7/dates/2026-11-04", nameDate = false) {
  render(<DateActions eventId={7} date="2026-11-04" sharePath={sharePath} title="Study" when="Wednesday, Nov 4 · 7:00–8:30 PM" nameDate={nameDate} />);
}

const original = { share: navigator.share, clipboard: Object.getOwnPropertyDescriptor(navigator, "clipboard") };

afterEach(() => {
  vi.useRealTimers();
  Object.defineProperty(navigator, "share", { value: original.share, configurable: true, writable: true });
  if (original.clipboard) Object.defineProperty(navigator, "clipboard", original.clipboard);
});

function setShare(value: unknown) {
  Object.defineProperty(navigator, "share", { value, configurable: true, writable: true });
}

describe("DateActions", () => {
  beforeEach(() => setShare(undefined));

  it("downloads the one-event calendar file", () => {
    renderActions();
    const link = screen.getByRole("link", { name: /^Add to calendar/ });
    expect(link).toHaveAttribute("href", "/api/v1/events/7/dates/2026-11-04/calendar.ics");
    expect(link).toHaveAttribute("download");
    expect(link).toHaveClass("min-h-11");
  });

  it("names the date for screen readers only when asked", () => {
    renderActions("/events/7", true);
    expect(screen.getByRole("link", { name: "Add to calendar for Wednesday, Nov 4 · 7:00–8:30 PM" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Share Wednesday, Nov 4 · 7:00–8:30 PM" })).toBeInTheDocument();
  });

  it("opens the share sheet with the title, when line and link when the device has one", async () => {
    const share = vi.fn().mockResolvedValue(undefined);
    setShare(share);
    renderActions();
    await userEvent.click(screen.getByRole("button", { name: /^Share/ }));
    expect(share).toHaveBeenCalledWith({
      title: "Study",
      text: "Wednesday, Nov 4 · 7:00–8:30 PM",
      url: `${window.location.origin}/events/7/dates/2026-11-04`,
    });
    expect(note()).toBeEmptyDOMElement();
  });

  it("stays quiet when the person closes the share sheet", async () => {
    setShare(vi.fn().mockRejectedValue(new DOMException("closed", "AbortError")));
    renderActions();
    await userEvent.click(screen.getByRole("button", { name: /^Share/ }));
    expect(note()).toBeEmptyDOMElement();
  });

  it("copies the link and says so for three seconds when there is no share sheet", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    renderActions("/events/7");
    await user.click(screen.getByRole("button", { name: /^Share/ }));
    expect(writeText).toHaveBeenCalledWith(`${window.location.origin}/events/7`);
    expect(note()).toHaveTextContent("Link copied");
    await act(async () => { await vi.advanceTimersByTimeAsync(3100); });
    expect(note()).toBeEmptyDOMElement();
  });

  it("shows the link to copy by hand when copying is blocked", async () => {
    Object.defineProperty(navigator, "clipboard", { value: { writeText: vi.fn().mockRejectedValue(new Error("no")) }, configurable: true });
    renderActions("/events/7");
    await userEvent.click(screen.getByRole("button", { name: /^Share/ }));
    expect(note()).toHaveTextContent(`Copy this link: ${window.location.origin}/events/7`);
  });
});
