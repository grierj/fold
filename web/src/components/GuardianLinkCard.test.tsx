import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const confirmLink = vi.fn();
vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  AdminGuardianLinks: { list: vi.fn(), confirm: (id: number, n: string) => confirmLink(id, n), reject: vi.fn() },
}));

import { ApiError } from "../api";
import { GuardianLinkCard } from "./GuardianLinkCard";

const link = {
  id: 9, guardianUserId: 1, guardianName: "Pat Smith", personId: 5, personName: "Kid Smith",
  personKind: "child" as const, birthDate: "2020-05-01", status: "pending" as const, checkNote: "", createdAt: "2026-10-21T00:00:00Z",
};

describe("GuardianLinkCard (M1.9, DEC-32, DEC-79)", () => {
  beforeEach(() => confirmLink.mockReset());

  it("needs a check note before confirming", async () => {
    const done = vi.fn();
    render(<GuardianLinkCard link={link} onDone={done} />);
    await userEvent.click(screen.getByRole("button", { name: "Confirm link" }));
    expect(screen.getByText("Write how you checked this link first.")).toBeInTheDocument();
    expect(confirmLink).not.toHaveBeenCalled();
  });

  it("shows the self-confirm message and keeps the item", async () => {
    confirmLink.mockRejectedValue(new ApiError(403, "x", { code: "self_confirm_blocked" }));
    const done = vi.fn();
    render(<GuardianLinkCard link={link} onDone={done} />);
    await userEvent.type(screen.getByLabelText(/How did you check/), "Known family");
    await userEvent.click(screen.getByRole("button", { name: "Confirm link" }));
    expect(await screen.findByText("Another admin needs to confirm your own household link.")).toBeInTheDocument();
    expect(done).not.toHaveBeenCalled();
  });
});
