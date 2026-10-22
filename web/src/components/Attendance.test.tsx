import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { Attendance } from "./Attendance";
import { occurrence } from "../test/occurrence";

describe("Attendance", () => {
  it("counts remote seats and marks remote attendees with a neutral badge", async () => {
    render(<Attendance instance={occurrence({
      remoteAvailable: true, remoteCapacity: 5, remoteSpotsLeft: 4,
      attendeesVisible: true,
      attendees: [{ userId: 3, name: "Sam", status: "going", guestCount: 0, attendanceMode: "remote" }],
    })} />);
    expect(screen.getByText(/4 of 5 remote seats left/)).toBeInTheDocument();
    expect(screen.getByText("Remote attendance available")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Show who's coming" }));
    expect(screen.getByText("Remote")).toHaveClass("bg-sunken", "text-text-muted");
    expect(screen.queryByText(/\(remote\)/)).not.toBeInTheDocument();
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
    attendeesVisible: true, attendees: [{ userId: 3, name: "Sam", status: "going", guestCount: 0,
      attendanceMode: "in_person", childcareCount: 2, childcareAgesNote: "3 and 5" }],
  })} />);
  expect(screen.getByText("2 of 5 childcare spots used")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Show who's coming" }));
  expect(screen.getByText(/Sam.*\+2 in childcare.*Ages: 3 and 5/)).toBeInTheDocument();
});

it("does not invent childcare totals when they are omitted", () => {
  render(<Attendance instance={occurrence({ childcareAvailable: true, childcareCapacity: 5 })} />);
  expect(screen.queryByText(/childcare spots used/)).not.toBeInTheDocument();
});
