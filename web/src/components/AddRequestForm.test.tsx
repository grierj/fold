import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { Adds } from "../add-api";
import { ApiError } from "../api";
import { addRequest } from "../test/addRequest";
import { AddRequestForm } from "./AddRequestForm";

vi.mock("../add-api", () => ({ Adds: { create: vi.fn() } }));
const person = { selectionId: "opaque", displayName: "Teen Pat", teen: true, sharedGroups: [], otherGroups: [] };
beforeEach(() => { vi.resetAllMocks(); vi.mocked(Adds.create).mockResolvedValue({ request: addRequest() }); vi.spyOn(window, "confirm").mockReturnValue(true); });
function mount(group = true, full = false) { render(<MemoryRouter><AddRequestForm person={person} scope={group ? { contextType: "group", contextId: 3 } : { contextType: "event", contextId: 4, occurrenceId: 41, attendanceMode: "remote", guestCount: 2 }} full={full} onClose={() => {}} /></MemoryRouter>); }

it.each([true, false])("sends explicit %s group/date asks, not direct contact, without implicit override", async (group) => {
  mount(group, true);
  await userEvent.type(screen.getByRole("textbox", { name: /Reason/ }), "Welcome");
  await userEvent.click(screen.getByRole("button", { name: "Send ask to add" }));
  expect(Adds.create).toHaveBeenCalledWith(group ? { selectionId: "opaque", contextType: "group", contextId: 3, reason: "Welcome", overLimit: false } : { selectionId: "opaque", contextType: "event", contextId: 4, occurrenceId: 41, attendanceMode: "remote", guestCount: 2, reason: "Welcome", overCapacity: false });
  expect(screen.getByRole("status")).toHaveTextContent("They have not been added yet");
  expect(screen.getByRole("link", { name: "View ask" })).toHaveAttribute("href", "/add-requests/7");
  expect(window.confirm).not.toHaveBeenCalled();
});

it("counts Unicode characters and preserves reason/options on validation and server failure", async () => {
  mount(false, true);
  const reason = screen.getByRole("textbox", { name: /Reason/ });
  fireEvent.change(reason, { target: { value: "😀".repeat(1001) } });
  await userEvent.click(screen.getByRole("button", { name: "Send ask to add" }));
  expect(reason).toHaveFocus(); expect(Adds.create).not.toHaveBeenCalled();
  fireEvent.change(reason, { target: { value: "😀".repeat(1000) } });
  await userEvent.click(screen.getByRole("checkbox"));
  vi.mocked(Adds.create).mockRejectedValue(new ApiError(409, "SECRET", { code: "request_open_limit" }));
  await userEvent.click(screen.getByRole("button", { name: "Send ask to add" }));
  expect(reason).toHaveValue("😀".repeat(1000)); expect(screen.getByRole("checkbox")).toBeChecked();
  expect(Adds.create).toHaveBeenCalledWith(expect.objectContaining({ overCapacity: true, attendanceMode: "remote", guestCount: 2 }));
  expect(screen.getByRole("alert")).toHaveTextContent("10 open requests"); expect(screen.getByRole("alert")).not.toHaveTextContent("SECRET");
});

it("requires confirmation for an explicit override and keeps the form on cancel", async () => {
  mount(true, true); vi.mocked(window.confirm).mockReturnValue(false);
  await userEvent.click(screen.getByRole("checkbox")); await userEvent.click(screen.getByRole("button", { name: "Send ask to add" }));
  expect(window.confirm).toHaveBeenCalled(); expect(Adds.create).not.toHaveBeenCalled(); expect(screen.getByRole("checkbox")).toBeChecked();
});
