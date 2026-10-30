import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { Attendance } from "./Attendance";
import { occurrence } from "../test/occurrence";

describe("Attendance", () => {
  it("uses singular and full seat wording (PR 110 N4)", () => {
    const { rerender } = render(<Attendance instance={occurrence({ capacity: 1, spotsLeft: 1 })} />);
    expect(screen.getByText("Only 1 seat left")).toBeInTheDocument();
    rerender(<Attendance instance={occurrence({ capacity: 1, spotsLeft: 0, remoteAvailable: true, remoteCapacity: 2, remoteSpotsLeft: 0 })} />);
    expect(screen.getByText(/Seats full · Remote seats full/)).toBeInTheDocument();
  });

  it("warns in words at five seats or fewer, and counts plainly above that (DEC-99)", () => {
    const { rerender } = render(<Attendance instance={occurrence({ capacity: 20, spotsLeft: 5 })} />);
    const warn = screen.getByText("Only 5 seats left");
    expect(warn).toHaveClass("text-warning-on-subtle");
    rerender(<Attendance instance={occurrence({ capacity: 20, spotsLeft: 6 })} />);
    expect(screen.queryByText(/Only/)).not.toBeInTheDocument();
    expect(screen.getByText(/6 of 20 seats left/)).toBeInTheDocument();
    rerender(<Attendance instance={occurrence({ capacity: 20, spotsLeft: 0 })} />);
    expect(screen.getByText(/Seats full/)).toBeInTheDocument();
  });

  it("leaves the remote seat line alone", () => {
    render(<Attendance instance={occurrence({ remoteAvailable: true, remoteCapacity: 10, remoteSpotsLeft: 2 })} />);
    expect(screen.getByText(/2 of 10 remote seats left/)).toBeInTheDocument();
    expect(screen.queryByText(/Only/)).not.toBeInTheDocument();
  });

  it("counts remote seats and marks remote attendees with a neutral badge", async () => {
    render(<Attendance instance={occurrence({
      remoteAvailable: true, remoteCapacity: 5, remoteSpotsLeft: 4,
      attendeesVisible: true,
      attendees: [{ registrationId: 31, userId: 3, name: "Sam", status: "going", guestCount: 0, attendanceMode: "remote" }],
    })} />);
    expect(screen.getByText(/4 of 5 remote seats left/)).toBeInTheDocument();
    expect(screen.queryByText("Remote attendance available")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Show who's coming" }));
    expect(screen.getByText("Remote")).toHaveClass("bg-sunken", "text-text-muted");
    expect(screen.queryByText(/\(remote\)/)).not.toBeInTheDocument();
  });

  it("advertises remote attendance when the date has no remote seat limit", () => {
    render(<Attendance instance={occurrence({ remoteAvailable: true, remoteCapacity: null })} />);
    expect(screen.getByText("Remote attendance available")).toBeInTheDocument();
  });

  it("labels visible going, maybe and declined names without inventing private people", async () => {
    render(<Attendance instance={occurrence({
      registrations: { going: 3, maybe: 1, declined: 1, waitlisted: 0 }, attendeesVisible: true,
      attendees: [
        { registrationId: 1, userId: 1, name: "Sam", status: "going", guestCount: 0, attendanceMode: "in_person" },
        { registrationId: 2, userId: 2, name: "Pat", status: "maybe", guestCount: 0, attendanceMode: "in_person" },
        { registrationId: 3, userId: 3, name: "Quinn", status: "declined", guestCount: 0, attendanceMode: "in_person" },
      ],
    })} />);
    expect(screen.getByText(/3 going · 1 maybe/)).toBeInTheDocument();
    expect(screen.getByText("Some people's names are private.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Show who's coming" }));
    for (const name of ["Going (1)", "Maybe (1)", "Can't go (1)"]) {
      expect(screen.getByRole("heading", { name, level: 4 })).toBeInTheDocument();
    }
    expect(screen.getAllByRole("listitem")).toHaveLength(3);
  });

  it("does not advertise remote attendance on a date with zero remote seats", () => {
    render(<Attendance instance={occurrence({ remoteAvailable: true, remoteCapacity: 0, remoteSpotsLeft: 0 })} />);
    expect(screen.queryByText("Remote attendance available")).not.toBeInTheDocument();
    expect(screen.getByText(/No remote seats on this date/)).toBeInTheDocument();
    expect(screen.queryByText(/0 of 0/)).not.toBeInTheDocument();
  });
});

it("shows childcare totals and requests supplied to a manager", async () => {
  render(<Attendance instance={occurrence({ childcareAvailable: true, childcareUsed: 2, childcareCapacity: 5,
    attendeesVisible: true, attendees: [{ registrationId: 31, userId: 3, name: "Sam", status: "going", guestCount: 0,
      attendanceMode: "in_person", childcareCount: 2, childcareAgesNote: "3 and 5" }],
  })} />);
  expect(screen.getByText("3 of 5 childcare spots left")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Show who's coming" }));
  expect(screen.getByText("Sam")).toBeInTheDocument();
  expect(screen.getByText("+2 childcare")).toHaveClass("bg-sunken", "text-text-muted");
  expect(screen.getByText("Ages: 3 and 5")).toHaveClass("line-clamp-3", "whitespace-pre-wrap");
});

it("does not invent childcare totals when they are omitted", () => {
  render(<Attendance instance={occurrence({ childcareAvailable: true, childcareCapacity: 5 })} />);
  expect(screen.queryByText(/childcare spots left|children in childcare/)).not.toBeInTheDocument();
});

it("shows full childcare to a viewer without private totals or names", () => {
  render(<Attendance instance={occurrence({ childcareAvailable: true, childcareCapacity: 2, childcareFull: true })} />);
  expect(screen.getByText("Childcare is full for this date")).toBeInTheDocument();
  expect(screen.queryByText(/childcare spots left/)).not.toBeInTheDocument();
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
});

it("shows an unlimited childcare total as children", () => {
  render(<Attendance instance={occurrence({ childcareAvailable: true, childcareUsed: 4 })} />);
  expect(screen.getByText("4 children in childcare")).toBeInTheDocument();
  expect(screen.queryByText(/unlimited|childcare spots/)).not.toBeInTheDocument();
});

it("never shows a negative number of childcare spots left", () => {
  render(<Attendance instance={occurrence({ childcareAvailable: true, childcareUsed: 4, childcareCapacity: 2, childcareFull: true })} />);
  expect(screen.getByText("0 of 2 childcare spots left")).toBeInTheDocument();
});
