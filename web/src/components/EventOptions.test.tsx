import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { Site } from "../api/site";
import { emptyChildcare } from "./ChildcareField";
import { EventOptions } from "./EventOptions";
import { emptyEventOptions, type EventOptionValues } from "./eventOptionValues";
import { emptySeats } from "./SeatsField";
import { emptyVideo } from "./VideoField";
import type { Visibility } from "../types";

vi.mock("../api/events", () => ({ Events: { generateJitsi: vi.fn() } }));
vi.mock("../api/site", () => ({ Site: { get: vi.fn(() => Promise.resolve({ pushEnabled: true })) } }));

const base: EventOptionValues = { ...emptyEventOptions(), reminderPush: false };

// The line a closed "More options" shows, read as its button's description.
function closedSummary(values: EventOptionValues, visibility?: Visibility): string | null {
  render(<EventOptions idPrefix="s" open={false} onOpenChange={() => {}} values={values} onChange={() => {}}
    visibility={visibility ? { value: visibility, field: null } : undefined} />);
  return document.getElementById(screen.getByRole("button", { name: "More options" }).getAttribute("aria-describedby")!)!.textContent;
}

describe("the closed summary", () => {
  it("says what is set, so nothing is hidden by surprise", () => {
    expect(closedSummary(base, "public")).toBe("Visible to: Public · No seat limit · No video · No childcare · Reminder 1 day before");
  });
  it("names each choice", () => {
    expect(
      closedSummary({
        ...base,
        seats: { capacity: "20", waitlist: true, guests: 0 },
        video: { ...emptyVideo, provider: "jitsi" },
        remote: { available: true, capacity: "" },
        childcare: { ...emptyChildcare, available: true },
      }, "invite_only"),
    ).toBe("Visible to: Invite only · 20 seats with waitlist · Video link · Remote seats · Childcare · Reminder 1 day before");
  });
  it("counts one seat in the singular", () => {
    expect(closedSummary({ ...base, seats: { ...emptySeats, capacity: "1" } })).toBe("1 seat · No video · No childcare · Reminder 1 day before");
  });
  it("says when there is no reminder", () => {
    expect(closedSummary({ ...base, reminder: 0 })).toBe("No seat limit · No video · No childcare · No reminder");
  });
});

function Harness({ open = false }: { open?: boolean }) {
  const [isOpen, setOpen] = useState(open);
  const [values, setValues] = useState<EventOptionValues>(base);
  return <EventOptions idPrefix="t" open={isOpen} onOpenChange={setOpen} values={values} onChange={setValues} />;
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
  const [values, setValues] = useState<EventOptionValues>({ ...base, reminderPush: true });
  return (
    <>
      <EventOptions idPrefix="p" open onOpenChange={() => {}} values={values} onChange={setValues} />
      <p>push is {values.reminderPush ? "on" : "off"}</p>
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

  it("joins the closed summary", async () => {
    const { unmount } = render(<EventOptions idPrefix="s" open={false} onOpenChange={() => {}} values={{ ...base, reminderPush: true }} onChange={() => {}} />);
    await waitFor(() => expect(screen.getByText("No seat limit · No video · No childcare · Reminder 1 day before · Push reminder")).toBeInTheDocument());
    unmount();
    render(<EventOptions idPrefix="s" open={false} onOpenChange={() => {}} values={{ ...base, reminder: 0, reminderPush: true }} onChange={() => {}} />);
    await waitFor(() => expect(screen.getByText("No seat limit · No video · No childcare · No reminder email · Push reminder")).toBeInTheDocument());
  });
});
