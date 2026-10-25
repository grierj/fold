import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { Contacts } from "../contact-api";
import { ApiError } from "../api";
import type { ContactGrant, ContactReveal } from "../types";
import { ContactGrantCard } from "./ContactGrantCard";

vi.mock("../contact-api", () => ({ Contacts: { reveal: vi.fn(), revoke: vi.fn() } }));
const grant: ContactGrant = { id: 4, requestId: 5, requesterId: 1, requesterName: "Lee", subjectUserId: 2, subjectName: "Pat", contextType: "group", contextId: 3, fields: "both", needsAdmin: false, grantedAt: "2026-10-01T12:00:00Z", endReason: "", canReveal: true, canRevoke: true };
beforeEach(() => { vi.clearAllMocks(); vi.spyOn(window, "confirm").mockReturnValue(true); });
const mount = (g = grant, onChanged = vi.fn()) => render(<MemoryRouter><ContactGrantCard grant={g} onChanged={onChanged} /></MemoryRouter>);

it("does not reveal on GET/review; explicit POST echoes exact scope and clears on close", async () => {
  vi.mocked(Contacts.reveal).mockResolvedValue({ email: "private@example.com" });
  mount();
  expect(Contacts.reveal).not.toHaveBeenCalled();
  await userEvent.click(screen.getByLabelText("Email"));
  await userEvent.click(screen.getByRole("button", { name: "Reveal contact details" }));
  expect(Contacts.reveal).toHaveBeenCalledWith(4, { subjectUserId: 2, contextType: "group", contextId: 3, fields: "email" });
  expect(await screen.findByRole("region", { name: "Revealed contact details" })).toHaveTextContent("private@example.com");
  await userEvent.click(screen.getByRole("button", { name: "Close contact details" }));
  expect(screen.queryByText(/private@example.com/)).not.toBeInTheDocument();
  expect(Contacts.reveal).toHaveBeenCalledTimes(1);
  expect(localStorage.length).toBe(0);
  expect(sessionStorage.length).toBe(0);
});

it("discards an in-flight reveal after close and clears values on unmount/remount", async () => {
  let resolve!: (v: ContactReveal) => void;
  vi.mocked(Contacts.reveal).mockReturnValue(new Promise((done) => { resolve = done; }));
  const view = mount();
  await userEvent.click(screen.getByRole("button", { name: "Reveal contact details" }));
  await userEvent.click(screen.getByRole("button", { name: "Close contact details" }));
  await act(async () => resolve({ phone: "555-SECRET" }));
  expect(screen.queryByText(/555-SECRET/)).not.toBeInTheDocument();
  vi.mocked(Contacts.reveal).mockResolvedValue({ phone: "555-SECRET" });
  await userEvent.click(screen.getByRole("button", { name: "Reveal contact details" }));
  expect(await screen.findByText("Phone: 555-SECRET")).toBeInTheDocument();
  view.unmount(); mount();
  expect(screen.queryByText(/555-SECRET/)).not.toBeInTheDocument();
});

it("clears revealed values before revocation, even when revocation fails", async () => {
  vi.mocked(Contacts.reveal).mockResolvedValue({ email: "private@example.com", phone: "" });
  vi.mocked(Contacts.revoke).mockRejectedValue(new ApiError(403, "SECRET", { code: "contact_unavailable" }));
  mount();
  await userEvent.click(screen.getByRole("button", { name: "Reveal contact details" }));
  expect(await screen.findByText("Phone: Not provided")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Revoke access" }));
  expect(screen.queryByText(/private@example.com/)).not.toBeInTheDocument();
  expect(await screen.findByRole("alert")).toHaveTextContent("no longer available to you");
});

it("never invents a hidden context link or attempts reveal without its context ID", () => {
  mount({ ...grant, contextId: undefined });
  expect(screen.queryByRole("link")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Reveal contact details" })).not.toBeInTheDocument();
  expect(screen.getByText(/not currently visible/)).toBeInTheDocument();
  expect(Contacts.reveal).not.toHaveBeenCalled();
});

it("discards values when a grant ends or its capability is removed", async () => {
  vi.mocked(Contacts.reveal).mockResolvedValue({ email: "private@example.com" });
  const view = mount();
  await userEvent.click(screen.getByRole("button", { name: "Reveal contact details" }));
  await screen.findByText(/private@example.com/);
  view.rerender(<MemoryRouter><ContactGrantCard grant={{ ...grant, endedAt: "2026-10-02T12:00:00Z", canReveal: false, canRevoke: false, endReason: "revoked_by_subject" }} onChanged={vi.fn()} /></MemoryRouter>);
  expect(screen.queryByText(/private@example.com/)).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Reveal contact details" })).not.toBeInTheDocument();
});
