import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, RoomRequests } from "../../api";
import type { RoomRequest } from "../../types";
import { RoomRequestsList } from "./RoomRequestsList";

vi.mock("../../api", async (actual) => ({
  ...(await actual<typeof import("../../api")>()),
  RoomRequests: { list: vi.fn(), confirm: vi.fn(), reject: vi.fn() },
}));

// The tests run in America/Boise, where 19:00 UTC on Nov 10 is noon.
const series: RoomRequest = {
  id: 5, eventId: 9, eventTitle: "Weekly Bible study", groupName: "Room 201 class", roomId: 1, roomName: "Fellowship Hall", building: "Main",
  series: true, requestedBy: "Lee Park", dates: 40, busyDates: 0, firstDate: "2026-11-10", firstStartsAt: "2026-11-10T19:00:00Z", createdAt: "2026-10-30T15:00:00Z",
};
const single: RoomRequest = { ...series, id: 6, eventId: 10, eventTitle: "Potluck", groupName: "", series: false, dates: 1, firstDate: "2026-11-17", firstStartsAt: "2026-11-17T19:00:00Z" };

// The page owns the list and drops a decided request; this stands in for it.
function Harness({ initial, needApproval = true, reload = () => {} }: { initial: RoomRequest[] | null; needApproval?: boolean; reload?: () => void }) {
  const [requests, setRequests] = useState(initial);
  return (
    <MemoryRouter>
      <RoomRequestsList requests={requests} needApproval={needApproval} error={null} reload={reload}
        onRemoved={(id) => setRequests((prev) => prev && prev.filter((r) => r.id !== id))}>
        <p>The room catalog</p>
      </RoomRequestsList>
    </MemoryRouter>
  );
}
const row = (title: string) => screen.getByRole("heading", { name: title }).closest("li") as HTMLElement;
const btn = (within_: HTMLElement, name: string) => within(within_).getByRole("button", { name: new RegExp(`^${name}`) });

beforeEach(() => {
  vi.resetAllMocks();
});

describe("RoomRequestsList", () => {
  it("shows the event as a link, group, room, first date and time, and who asked", () => {
    render(<Harness initial={[series, single]} />);
    const a = row("Weekly Bible study");
    expect(within(a).getByRole("link", { name: "Weekly Bible study" })).toHaveAttribute("href", "/events/9");
    expect(within(a).getByText("Room 201 class")).toBeInTheDocument();
    expect(within(a).getByText("Fellowship Hall · Main")).toBeInTheDocument();
    expect(within(a).getByText("Every date from Tuesday, November 10 at 12:00 PM (40 dates)")).toBeInTheDocument();
    expect(within(a).getByText(/^Asked by Lee Park · /)).toBeInTheDocument();
    const b = row("Potluck");
    expect(within(b).getByText("Tuesday, November 17 at 12:00 PM")).toBeInTheDocument();
    expect(within(b).queryByText("Room 201 class")).not.toBeInTheDocument();
  });

  it("names its buttons for the event, so a list of them is not a list of identical words", () => {
    render(<Harness initial={[series]} />);
    expect(screen.getByRole("button", { name: "Approve for Weekly Bible study" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reject for Weekly Bible study" })).toBeInTheDocument();
  });

  it("approves a request with no busy dates at once, announces it with the event, and drops the row", async () => {
    vi.mocked(RoomRequests.confirm).mockResolvedValue({ confirmed: 40, overlapped: 0, lost: [] });
    render(<Harness initial={[series, single]} />);
    await userEvent.click(btn(row("Weekly Bible study"), "Approve"));
    expect(RoomRequests.confirm).toHaveBeenCalledWith(5, undefined);
    const status = await screen.findByText("Approved Fellowship Hall · Main for Weekly Bible study: 40 dates.");
    expect(status).toHaveFocus();
    expect(screen.getByRole("status")).toBe(status);
    expect(screen.queryByRole("heading", { name: "Weekly Bible study" })).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Potluck" })).toBeInTheDocument();
  });

  it("moves the cursor to the message even when a second decision reads the same", async () => {
    const same = { ...single, id: 7, eventId: 11 };
    vi.mocked(RoomRequests.confirm).mockResolvedValue({ confirmed: 1, overlapped: 0, lost: [] });
    render(<Harness initial={[single, same]} />);
    const rows = screen.getAllByRole("listitem");
    await userEvent.click(btn(rows[0], "Approve"));
    const first = await screen.findByText("Approved Fellowship Hall · Main for Potluck: 1 date.");
    expect(first).toHaveFocus();
    first.blur();
    await userEvent.click(btn(screen.getAllByRole("listitem")[0], "Approve"));
    await waitFor(() => expect(RoomRequests.confirm).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.getByRole("status")).toHaveFocus());
    expect(screen.getByRole("status")).toHaveTextContent("Approved Fellowship Hall · Main for Potluck: 1 date.");
  });

  describe("with busy dates", () => {
    const busy: RoomRequest = { ...series, busyDates: 2 };

    it("says so in words, and explains what Approve does before anything is sent", async () => {
      render(<Harness initial={[busy]} />);
      const r = row("Weekly Bible study");
      expect(within(r).getAllByText("2 of these dates are now taken.")).toHaveLength(1);
      await userEvent.click(btn(r, "Approve"));
      expect(RoomRequests.confirm).not.toHaveBeenCalled();
      // The panel says it, so the line above the buttons does not say it twice.
      expect(within(r).getAllByText(/2 of these dates are now taken\./)).toHaveLength(1);
      expect(within(r).getByText(/Without a reason, they go back to the people running it to pick another room\./)).toBeInTheDocument();
      const reason = within(r).getByRole("textbox", { name: /^Double-book reason/ });
      expect(reason).toHaveAttribute("maxlength", "500");
      expect(reason).toHaveFocus();
      expect(reason).toHaveAccessibleDescription("Up to 500 characters. It goes in the admin record.");
    });

    it("sends those dates back when no reason is given", async () => {
      vi.mocked(RoomRequests.confirm).mockResolvedValue({ confirmed: 38, overlapped: 0, lost: ["2026-11-17", "2026-11-24"] });
      render(<Harness initial={[busy]} />);
      const r = row("Weekly Bible study");
      await userEvent.click(btn(r, "Approve"));
      await userEvent.click(btn(r, "Approve"));
      expect(RoomRequests.confirm).toHaveBeenCalledWith(5, undefined);
      expect(await screen.findByText("Approved Fellowship Hall · Main for Weekly Bible study: 38 dates. 2 dates went back to the people running it.")).toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: "Weekly Bible study" })).not.toBeInTheDocument();
    });

    it("double-books them when a reason is given", async () => {
      vi.mocked(RoomRequests.confirm).mockResolvedValue({ confirmed: 38, overlapped: 2, lost: [] });
      render(<Harness initial={[busy]} />);
      const r = row("Weekly Bible study");
      await userEvent.click(btn(r, "Approve"));
      await userEvent.type(within(r).getByRole("textbox", { name: /^Double-book reason/ }), "Choir moved");
      await userEvent.click(btn(r, "Approve"));
      expect(RoomRequests.confirm).toHaveBeenCalledWith(5, "Choir moved");
      expect(await screen.findByText("Approved Fellowship Hall · Main for Weekly Bible study: 40 dates, 2 of them double-booked.")).toBeInTheDocument();
    });

    it("goes back to the buttons, with the cursor on the one that opened the panel", async () => {
      render(<Harness initial={[busy]} />);
      const r = row("Weekly Bible study");
      await userEvent.click(btn(r, "Approve"));
      await userEvent.click(btn(r, "Back"));
      expect(btn(r, "Reject")).toBeInTheDocument();
      expect(within(r).queryByRole("textbox")).not.toBeInTheDocument();
      await waitFor(() => expect(btn(r, "Approve")).toHaveFocus());
      expect(RoomRequests.confirm).not.toHaveBeenCalled();
      expect(within(r).getAllByText("2 of these dates are now taken.")).toHaveLength(1);
    });

    it("says it plainly for a single date", () => {
      render(<Harness initial={[{ ...single, busyDates: 1 }]} />);
      expect(within(row("Potluck")).getByText("This date is now taken.")).toBeInTheDocument();
    });
  });

  describe("Reject", () => {
    it("takes an optional reason and sends it", async () => {
      vi.mocked(RoomRequests.reject).mockResolvedValue({});
      render(<Harness initial={[series, single]} />);
      const r = row("Weekly Bible study");
      await userEvent.click(btn(r, "Reject"));
      expect(RoomRequests.reject).not.toHaveBeenCalled();
      const why = within(r).getByRole("textbox", { name: /^Tell them why/ });
      expect(why).toHaveAttribute("maxlength", "500");
      expect(why).toHaveFocus();
      expect(why).toHaveAccessibleDescription("Up to 500 characters.");
      await userEvent.type(why, "The hall is closed for repairs");
      await userEvent.click(btn(r, "Reject and tell them"));
      expect(RoomRequests.reject).toHaveBeenCalledWith(5, "The hall is closed for repairs");
      expect(await screen.findByText("Rejected Fellowship Hall · Main for Weekly Bible study. The people running it were told.")).toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: "Weekly Bible study" })).not.toBeInTheDocument();
    });

    it("sends without a reason too, and Cancel sends nothing and returns the cursor", async () => {
      vi.mocked(RoomRequests.reject).mockResolvedValue({});
      render(<Harness initial={[series]} />);
      const r = row("Weekly Bible study");
      await userEvent.click(btn(r, "Reject"));
      await userEvent.click(btn(r, "Cancel"));
      expect(RoomRequests.reject).not.toHaveBeenCalled();
      await waitFor(() => expect(btn(r, "Reject")).toHaveFocus());
      await userEvent.click(btn(r, "Reject"));
      await userEvent.click(btn(r, "Reject and tell them"));
      expect(RoomRequests.reject).toHaveBeenCalledWith(5, undefined);
    });
  });

  describe("when someone else decided first (409)", () => {
    const conflict = (data: Record<string, unknown>) => new ApiError(409, "This was already handled", data);

    it("says who handled it, in this page's verbs, drops the row and asks for a fresh list", async () => {
      vi.mocked(RoomRequests.confirm).mockRejectedValue(conflict({ resolvedBy: "Pat Smith", resolution: "rejected" }));
      const reload = vi.fn();
      render(<Harness initial={[series]} reload={reload} />);
      await userEvent.click(btn(row("Weekly Bible study"), "Approve"));
      expect(await screen.findByText("Already rejected by Pat Smith.")).toBeInTheDocument();
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      expect(reload).toHaveBeenCalledTimes(1);
      expect(screen.queryByRole("heading", { name: "Weekly Bible study" })).not.toBeInTheDocument();
    });

    it("reads confirmed as approved, and an empty or missing outcome as handled", async () => {
      vi.mocked(RoomRequests.confirm).mockRejectedValueOnce(conflict({ resolvedBy: "Pat Smith", resolution: "confirmed" }));
      const { unmount } = render(<Harness initial={[series]} />);
      await userEvent.click(btn(row("Weekly Bible study"), "Approve"));
      expect(await screen.findByText("Already approved by Pat Smith.")).toBeInTheDocument();
      unmount();
      vi.mocked(RoomRequests.confirm).mockRejectedValueOnce(conflict({ resolvedBy: "", resolution: "" }));
      render(<Harness initial={[series]} />);
      await userEvent.click(btn(row("Weekly Bible study"), "Approve"));
      expect(await screen.findByText("Already handled by someone.")).toBeInTheDocument();
    });
  });

  it("keeps the row and says what went wrong on any other failure", async () => {
    vi.mocked(RoomRequests.confirm).mockRejectedValue(new ApiError(500, "private server text"));
    render(<Harness initial={[series]} />);
    const r = row("Weekly Bible study");
    await userEvent.click(btn(r, "Approve"));
    expect(await within(r).findByRole("alert")).toHaveTextContent("We couldn't approve that.");
    expect(screen.queryByText("private server text")).not.toBeInTheDocument();
    expect(btn(r, "Approve")).toBeEnabled();
  });

  it("shows the neutral empty state, then the catalog under a Rooms heading, while approval is on and nothing is waiting", () => {
    render(<Harness initial={[]} />);
    expect(screen.getByRole("heading", { name: "Needs a room" })).toBeInTheDocument();
    expect(screen.getByText("No bookings are waiting for approval.")).toBeInTheDocument();
    expect(screen.getByText("When a leader or host books a room, you'll approve or reject it here.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Rooms" })).toBeInTheDocument();
    expect(screen.getByText("The room catalog")).toBeInTheDocument();
  });

  it("shows only the catalog while approval is off and nothing is waiting, but still lists what is open", () => {
    const { unmount } = render(<Harness initial={[]} needApproval={false} />);
    expect(screen.getByText("The room catalog")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Needs a room" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Rooms" })).not.toBeInTheDocument();
    unmount();
    render(<Harness initial={[series]} needApproval={false} />);
    expect(screen.getByRole("heading", { name: "Weekly Bible study" })).toBeInTheDocument();
  });

  it("with approval off, deciding the last request leaves the message and the Rooms heading, and no false empty state", async () => {
    vi.mocked(RoomRequests.confirm).mockResolvedValue({ confirmed: 40, overlapped: 0, lost: [] });
    render(<Harness initial={[series]} needApproval={false} />);
    await userEvent.click(btn(row("Weekly Bible study"), "Approve"));
    expect(await screen.findByText("Approved Fellowship Hall · Main for Weekly Bible study: 40 dates.")).toBeInTheDocument();
    expect(screen.queryByText("No bookings are waiting for approval.")).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Rooms" })).toBeInTheDocument();
    expect(screen.getByText("The room catalog")).toBeInTheDocument();
  });

  it("offers a retry when the list fails to load, and still shows the catalog", async () => {
    const reload = vi.fn();
    render(
      <MemoryRouter>
        <RoomRequestsList requests={null} needApproval={true} error="We couldn't load the bookings waiting for approval." reload={reload} onRemoved={() => {}}>
          <p>The room catalog</p>
        </RoomRequestsList>
      </MemoryRouter>,
    );
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(reload).toHaveBeenCalled();
    expect(screen.queryByText("No bookings are waiting for approval.")).not.toBeInTheDocument();
    expect(screen.getByText("The room catalog")).toBeInTheDocument();
  });
});
