import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { Attendee } from "../types";
import { AttendeeDetails } from "./AttendeeDetails";

const attendee: Attendee = { registrationId: 9, name: "Kit", status: "going", guestCount: 0, attendanceMode: "in_person", childcareRequested: true, householdPerson: { personId: 3, name: "Kit", householdName: "Leader household", kind: "child", age: 6, registeredBy: { userId: 7, name: "John Smith" }, careNotes: "Allergy\n" + "LongCareNote".repeat(100) } };
afterEach(() => vi.useRealTimers());

it("shares household labels without inventing a surname, badges and authorized ages", () => {
  render(<AttendeeDetails attendee={attendee} />);
  expect(screen.getByText("Kit · Leader household")).toBeInTheDocument();
  expect(screen.getByText("Signed up by John Smith")).toBeInTheDocument();
  expect(screen.getByText("Child")).toBeInTheDocument(); expect(screen.getByText("Age 6")).toBeInTheDocument();
  expect(screen.getByText("+1 childcare")).toBeInTheDocument();
  expect(screen.queryByText("Kit Smith")).not.toBeInTheDocument();
});

it("clamps long notes and exposes an accessible disclosure", async () => {
  render(<AttendeeDetails attendee={attendee} />);
  const button = screen.getByRole("button", { name: "Show full care notes for Kit" });
  const note = document.getElementById(button.getAttribute("aria-controls") ?? "");
  expect(note).toHaveClass("line-clamp-3", "whitespace-pre-wrap", "wrap-anywhere");
  expect(button).toHaveAttribute("aria-expanded", "false");
  await userEvent.click(button);
  expect(note).not.toHaveClass("line-clamp-3"); expect(button).toHaveAttribute("aria-expanded", "true");
  expect(button).toHaveAccessibleName("Hide care notes");
});

it("clears notes when time-limited access expires", () => {
  vi.useFakeTimers(); vi.setSystemTime(new Date("2026-11-01T12:00:00Z"));
  const a = { ...attendee, householdPerson: { ...attendee.householdPerson!, careNotesVisibleUntil: "2026-11-01T12:00:01Z" } };
  render(<AttendeeDetails attendee={a} />);
  expect(screen.getByRole("button", { name: "Show full care notes for Kit" })).toBeInTheDocument();
  act(() => vi.advanceTimersByTime(1002));
  expect(screen.queryByText(/Allergy/)).not.toBeInTheDocument();
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
});

it("does not create missing private fields and handles a remote user separately", () => {
  render(<AttendeeDetails attendee={{ registrationId: 3, userId: 3, name: "Sam", status: "going", guestCount: 0, attendanceMode: "remote" }} />);
  expect(screen.getByText("Sam")).toBeInTheDocument(); expect(screen.getByText("Remote")).toBeInTheDocument();
  expect(screen.queryByText(/household|Age|Care notes/)).not.toBeInTheDocument(); expect(screen.queryByRole("button")).not.toBeInTheDocument();
});

it("leaves out a household name that only repeats the signer's surname", () => {
  render(<AttendeeDetails attendee={{ ...attendee, householdPerson: { ...attendee.householdPerson!, householdName: "Smith household" } }} />);
  expect(screen.getByText("Kit")).toBeInTheDocument();
  expect(screen.queryByText(/Smith household/)).not.toBeInTheDocument();
});

it("offers no toggle for a short note", () => {
  render(<AttendeeDetails attendee={{ ...attendee, householdPerson: { ...attendee.householdPerson!, careNotes: "Peanut allergy" } }} />);
  expect(screen.getByText("Care notes: Peanut allergy")).toBeInTheDocument();
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
});
