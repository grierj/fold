import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mine = vi.fn();
const addPerson = vi.fn();
vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  Households: { mine: () => mine(), addPerson: (d: unknown) => addPerson(d), update: vi.fn(), updatePerson: vi.fn(), removePerson: vi.fn() },
}));

import { HouseholdCard, personProblems } from "./HouseholdCard";

const household = {
  id: 1, name: "Smith household", address: "", createdBy: 1,
  accounts: [{ userId: 1, name: "Pat Smith", relation: "guardian" }],
  people: [{ id: 5, householdId: 1, firstName: "Kid", lastName: "Smith", name: "Kid Smith", kind: "child", birthDate: "2020-05-01", careNotes: "", photoConsent: false, linkStatus: "pending", createdBy: 1 }],
};

describe("HouseholdCard (M1.9)", () => {
  beforeEach(() => {
    mine.mockReset();
    addPerson.mockReset();
  });

  it("explains and offers to add when there is no household yet", async () => {
    mine.mockResolvedValue({ household: null });
    render(<HouseholdCard />);
    expect(await screen.findByText(/Add your children, teens/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add a household member" })).toBeInTheDocument();
  });

  it("lists people with their link status", async () => {
    mine.mockResolvedValue({ household });
    render(<HouseholdCard />);
    expect((await screen.findAllByText("Kid Smith")).length).toBeGreaterThan(0);
    expect(screen.getByText("Waiting for an admin to check")).toBeInTheDocument();
  });

  it("checks the form before sending", async () => {
    mine.mockResolvedValue({ household: null });
    render(<HouseholdCard />);
    await userEvent.click(await screen.findByRole("button", { name: "Add a household member" }));
    await userEvent.click(screen.getByRole("button", { name: "Add to household" }));
    expect(screen.getByText("Add a first name.")).toBeInTheDocument();
    expect(screen.getByText("Add a birth date for a child or teen.")).toBeInTheDocument();
    expect(addPerson).not.toHaveBeenCalled();
  });
});

describe("personProblems", () => {
  const base = { firstName: "A", lastName: "", kind: "adult" as const, birthDate: "", careNotes: "", photoConsent: false };
  it("lets an adult skip the birth date", () => {
    expect(personProblems(base, "2026-10-21")).toEqual({});
  });
  it("refuses a future birth date", () => {
    expect(personProblems({ ...base, birthDate: "2030-01-01" }, "2026-10-21").birthDate).toMatch(/future/);
  });
});
