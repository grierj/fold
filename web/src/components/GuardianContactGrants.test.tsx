import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { Contacts } from "../contact-api";
import type { ContactGrant } from "../types";
import { GuardianContactGrants } from "./GuardianContactGrants";
vi.mock("../contact-api", () => ({ Contacts: { grants: vi.fn(), reveal: vi.fn(), revoke: vi.fn() } }));
const grant: ContactGrant = { id: 1, requestId: 1, requesterId: 10, requesterName: "Lee", subjectUserId: 2, subjectName: "Teen Pat", contextType: "group", fields: "email", needsAdmin: true, grantedAt: "2026-10-02T12:00:00Z", endReason: "", canReveal: false, canRevoke: true };
beforeEach(() => vi.clearAllMocks());
it("shows only this teen's active teen-approved grants, never pending review or ended history", async () => {
  vi.mocked(Contacts.grants).mockResolvedValue({ grants: [grant, { ...grant, id: 2, subjectName: "Ended teen", endedAt: "2026-10-03T12:00:00Z" }, { ...grant, id: 3, subjectUserId: 4, subjectName: "Another teen" }, { ...grant, id: 4, needsAdmin: false, subjectName: "Adult Pat" }] });
  render(<MemoryRouter><GuardianContactGrants teenUserId={2} /></MemoryRouter>);
  expect(await screen.findByRole("button", { name: "Revoke access" })).toBeInTheDocument();
  expect(screen.queryByText(/Ended teen|Another teen|Adult Pat/)).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Approve sharing" })).not.toBeInTheDocument();
  expect(Contacts.reveal).not.toHaveBeenCalled();
});
