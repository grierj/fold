import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { Site } from "../api";
import { emptyChildcare } from "./ChildcareField";
import { EventOptions, optionsSummary } from "./EventOptions";
import { emptyRemote } from "./RemoteField";
import { emptySeats } from "./SeatsField";
import { emptyVideo } from "./VideoField";

vi.mock("../api", () => ({ Events: { generateJitsi: vi.fn() }, Site: { get: vi.fn(() => Promise.resolve({ pushEnabled: true })) } }));

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
    expect(optionsSummary({ ...base, reminder: 1440 })).toBe("No seat limit · No video · No childcare · Reminder 1 day before");
    expect(optionsSummary({ ...base, reminder: 0 })).toBe("No seat limit · No video · No childcare · No reminder");
  });
});

function Harness({ open = false }: { open?: boolean }) {
  const [isOpen, setOpen] = useState(open);
  const [seats, setSeats] = useState(emptySeats);
  const [video, setVideo] = useState(emptyVideo);
  const [remote, setRemote] = useState(emptyRemote);
  const [childcare, setChildcare] = useState(emptyChildcare);
  const [reminder, setReminder] = useState(1440);
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
      reminder={reminder}
      onReminder={setReminder}
    />
  );
}

describe("EventOptions", () => {
  it("is a disclosure: closed with a summary, open with the sections in order", async () => {
    render(<Harness />);
    const toggle = screen.getByRole("button", { name: "More options" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(toggle).toHaveAccessibleDescription("No seat limit · No video · No childcare · Reminder 1 day before");
    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.queryByText("No seat limit · No video · No childcare · Reminder 1 day before")).not.toBeInTheDocument();
    const region = document.getElementById(toggle.getAttribute("aria-controls")!)!;
    expect(region).toBeVisible();
    const titles = Array.from(region.querySelectorAll("h3")).map((h) => h.textContent);
    expect(titles).toEqual(["Seats", "Online", "Childcare", "Reminders"]);
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
    expect(screen.getByText("8 seats · No video · Childcare · Reminder 1 day before")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "More options" }));
    expect(screen.getByLabelText(/^Seat limit/)).toHaveValue(8);
  });
});

describe("ReminderField in More options", () => {
  it("offers the five reminder times, 1 day before by default, and says who hears", async () => {
    render(<Harness open />);
    const select = screen.getByLabelText("Reminder email");
    expect(select).toHaveValue("1440");
    expect(select).toHaveAccessibleDescription("We'll email everyone going. People on the waitlist hear they're still waiting.");
    expect(Array.from((select as HTMLSelectElement).options).map((o) => o.text)).toEqual(["Off", "2 hours before", "1 day before", "2 days before", "1 week before"]);
    await userEvent.selectOptions(select, "Off");
    await userEvent.click(screen.getByRole("button", { name: "More options" }));
    expect(screen.getByText("No seat limit · No video · No childcare · No reminder")).toBeInTheDocument();
  });
});

function PushHarness() {
  const [reminder, setReminder] = useState(1440);
  const [push, setPush] = useState(true);
  return (
    <>
      <EventOptions idPrefix="p" open onOpenChange={() => {}} {...base} onSeats={() => {}} onVideo={() => {}} onRemote={() => {}} onChildcare={() => {}}
        reminder={reminder} onReminder={setReminder} reminderPush={push} onReminderPush={setPush} />
      <p>push is {push ? "on" : "off"}</p>
    </>
  );
}

describe("the push reminder switch (M3.13d3)", () => {
  it("sits under the reminder email, on by default, and says who gets it", async () => {
    render(<PushHarness />);
    const box = await screen.findByRole("checkbox", { name: /^Push reminder 2 hours before/ });
    expect(box).toBeChecked();
    expect(box).toHaveAccessibleName(/People who turned on notifications get a short reminder on their phone\./);
    expect(screen.getByLabelText("Reminder email").compareDocumentPosition(box) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    await userEvent.click(box);
    expect(screen.getByText("push is off")).toBeInTheDocument();
  });

  it("is hidden when the church turned notifications off", async () => {
    vi.mocked(Site.get).mockResolvedValueOnce({ pushEnabled: false } as never);
    render(<PushHarness />);
    await waitFor(() => expect(screen.queryByRole("checkbox", { name: /Push reminder/ })).not.toBeInTheDocument());
    expect(screen.getByLabelText("Reminder email")).toBeInTheDocument();
  });

  it("joins the closed summary", () => {
    expect(optionsSummary({ ...base, reminder: 1440, reminderPush: true })).toBe("No seat limit · No video · No childcare · Reminder 1 day before · Push reminder");
    expect(optionsSummary({ ...base, reminder: 0, reminderPush: true })).toBe("No seat limit · No video · No childcare · No reminder email · Push reminder");
    expect(optionsSummary({ ...base, reminder: 0, reminderPush: false })).toBe("No seat limit · No video · No childcare · No reminder");
  });
});
