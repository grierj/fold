import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { GuardianConsentCard } from "./GuardianConsentCard";
import { Guardians } from "../guardian-api";

vi.mock("../guardian-api", () => ({ Guardians: { teens: vi.fn(), claim: vi.fn(), change: vi.fn(), email: vi.fn() } }));

beforeEach(() => vi.resetAllMocks());

test("a failed guardian list can be retried without reloading profile", async () => {
  vi.mocked(Guardians.teens).mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce({ teens: [{ linkId: 1, teenUserId: 2, teenName: "Sam Teen", accountConsent: false, smsConsent: false, photosConsent: false }] });
  render(<GuardianConsentCard />);
  await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
  expect(await screen.findByRole("heading", { name: "Sam Teen" })).toBeVisible();
  expect(screen.getByRole("button", { name: "Grant consent" })).toBeEnabled();
  expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
});
