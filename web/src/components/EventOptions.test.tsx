import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { emptyChildcare } from "./ChildcareField";
import { EventOptions, optionsSummary } from "./EventOptions";
import { emptyRemote } from "./RemoteField";
import { emptySeats } from "./SeatsField";
import { emptyVideo } from "./VideoField";

vi.mock("../api", () => ({ Events: { generateJitsi: vi.fn() } }));

const base = { seats: emptySeats, video: emptyVideo, remote: emptyRemote, childcare: emptyChildcare };

describe("optionsSummary", () => {
  it("says what is set, so nothing is hidden by surprise", () => {
    expect(optionsSummary({ ...base, visibility: "public" })).toBe("Visible to: Public · No seat limit · No video · No childcare");
    expect(
      optionsSummary({
        visibility: "invite_only",
        seats: { capacity: "20", waitlist: true, guests: 0 },
        video: { ...emptyVideo, provider: "jitsi" },
        remote: { available: true, capacity: "" },
        childcare: { ...emptyChildcare, available: true },
      }),
    ).toBe("Visible to: Invite only · 20 seats with waitlist · Video link · Remote seats · Childcare");
    expect(optionsSummary({ ...base, seats: { ...emptySeats, capacity: "1" } })).toBe("1 seat · No video · No childcare");
  });
});

function Harness({ open = false }: { open?: boolean }) {
  const [isOpen, setOpen] = useState(open);
  const [seats, setSeats] = useState(emptySeats);
  const [video, setVideo] = useState(emptyVideo);
  const [remote, setRemote] = useState(emptyRemote);
  const [childcare, setChildcare] = useState(emptyChildcare);
  return (
    <EventOptions
      idPrefix="t"
      open={isOpen}
      onOpenChange={setOpen}
      seats={seats}
      onSeats={setSeats}
      video={video}
      onVideo={setVideo}
      remote={remote}
      onRemote={setRemote}
      childcare={childcare}
      onChildcare={setChildcare}
    />
  );
}

describe("EventOptions", () => {
  it("is a disclosure: closed with a summary, open with the sections in order", async () => {
    render(<Harness />);
    const toggle = screen.getByRole("button", { name: "More options" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(toggle).toHaveAccessibleDescription("No seat limit · No video · No childcare");
    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.queryByText("No seat limit · No video · No childcare")).not.toBeInTheDocument();
    const region = document.getElementById(toggle.getAttribute("aria-controls")!)!;
    expect(region).toBeVisible();
    const titles = Array.from(region.querySelectorAll("h3")).map((h) => h.textContent);
    expect(titles).toEqual(["Seats", "Online", "Childcare"]);
    // Video and remote sit together under Online.
    const online = region.querySelectorAll('[role="group"]')[1];
    expect(online).toHaveTextContent("Video");
    expect(online).toHaveTextContent("Remote attendance");
  });

  it("keeps values while closed and reflects them in the summary", async () => {
    render(<Harness open />);
    await userEvent.type(screen.getByLabelText(/^Seat limit/), "8");
    await userEvent.click(screen.getByLabelText(/^Childcare available/));
    await userEvent.click(screen.getByRole("button", { name: "More options" }));
    expect(screen.getByText("8 seats · No video · Childcare")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "More options" }));
    expect(screen.getByLabelText(/^Seat limit/)).toHaveValue(8);
  });
});
