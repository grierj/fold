import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const getFeed = vi.fn();
const makeFeed = vi.fn();
const revokeFeed = vi.fn();
vi.mock("../../api", async (orig) => ({
  ...(await orig<typeof import("../../api")>()),
  AdminRooms: {
    getFeed: (id: number) => getFeed(id),
    makeFeed: (id: number) => makeFeed(id),
    revokeFeed: (id: number) => revokeFeed(id),
  },
}));

import { ApiError } from "../../api";
import type { Room } from "../../types";
import { RoomFeedPanel } from "./RoomFeedPanel";

const hall: Room = {
  id: 7, name: "Fellowship Hall", building: "Main", capacity: 80, features: "", notes: "",
  active: true, restricted: false, defaultSetupMinutes: 0, defaultTeardownMinutes: 0,
};
const URL_ = "https://church.example/cal/rooms/7/s3cret.ics";

describe("RoomFeedPanel (M2.2b)", () => {
  beforeEach(() => {
    getFeed.mockReset();
    makeFeed.mockReset();
    revokeFeed.mockReset();
  });
  afterEach(() => vi.restoreAllMocks());

  it("a failed load offers Try again, never Create", async () => {
    getFeed.mockRejectedValueOnce(new ApiError(500, "boom"));
    getFeed.mockResolvedValueOnce({ feed: { hasToken: true, createdAt: "2026-10-01T15:00:00Z" } });
    render(<RoomFeedPanel room={hall} onClose={() => {}} />);
    const retry = await screen.findByRole("button", { name: "Try again" });
    expect(screen.queryByRole("button", { name: "Create calendar link" })).toBeNull();
    await userEvent.click(retry);
    expect(await screen.findByRole("button", { name: "Make a new link" })).toBeInTheDocument();
    expect(screen.getByText(/A calendar link is on, made/)).toBeInTheDocument();
  });

  it("makes a link and shows it once", async () => {
    getFeed.mockResolvedValue({ feed: { hasToken: false, createdAt: null } });
    makeFeed.mockResolvedValue({ feed: { hasToken: true, createdAt: "2026-10-30T15:00:00Z", url: URL_ } });
    const { unmount } = render(<RoomFeedPanel room={hall} onClose={() => {}} />);
    expect(await screen.findByText(/doesn.t have a calendar link yet/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Create calendar link" }));
    expect(makeFeed).toHaveBeenCalledWith(7);
    expect(await screen.findByLabelText("Calendar link")).toHaveValue(URL_);
    expect(screen.getByText(/Save this link now; we won't show it again/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy link" })).toBeInTheDocument();
    unmount();

    // Opened again, the link is on but never shown.
    getFeed.mockResolvedValue({ feed: { hasToken: true, createdAt: "2026-10-30T15:00:00Z" } });
    render(<RoomFeedPanel room={hall} onClose={() => {}} />);
    expect(await screen.findByText(/A calendar link is on/)).toBeInTheDocument();
    expect(screen.queryByDisplayValue(URL_)).toBeNull();
  });

  it("a new link and turning it off both ask first", async () => {
    getFeed.mockResolvedValue({ feed: { hasToken: true, createdAt: "2026-10-30T15:00:00Z" } });
    makeFeed.mockResolvedValue({ feed: { hasToken: true, createdAt: "2026-10-30T16:00:00Z", url: URL_ } });
    revokeFeed.mockResolvedValue({ feed: { hasToken: false, createdAt: null } });
    const ask = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValue(true);
    render(<RoomFeedPanel room={hall} onClose={() => {}} />);

    await userEvent.click(await screen.findByRole("button", { name: "Make a new link" }));
    expect(ask).toHaveBeenCalledWith(expect.stringMatching(/old link will stop working/));
    expect(makeFeed).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Make a new link" }));
    expect(await screen.findByLabelText("Calendar link")).toHaveValue(URL_);

    await userEvent.click(screen.getByRole("button", { name: "Turn off calendar link" }));
    expect(revokeFeed).toHaveBeenCalledWith(7);
    expect(await screen.findByText("Calendar link turned off.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create calendar link" })).toBeInTheDocument();
    expect(screen.queryByDisplayValue(URL_)).toBeNull();
  });

  it("Done closes the panel", async () => {
    getFeed.mockResolvedValue({ feed: { hasToken: false, createdAt: null } });
    const onClose = vi.fn();
    render(<RoomFeedPanel room={hall} onClose={onClose} />);
    await userEvent.click(await screen.findByRole("button", { name: "Done" }));
    expect(onClose).toHaveBeenCalled();
  });
});
