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
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
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

describe("HouseholdCard focus and states (PR 107 review)", () => {
  beforeEach(() => {
    mine.mockReset();
    addPerson.mockReset();
  });

  it("moves focus to the first bad field on a failed check", async () => {
    mine.mockResolvedValue({ household: null });
    render(<HouseholdCard />);
    await userEvent.click(await screen.findByRole("button", { name: "Add a household member" }));
    expect(screen.getByLabelText("First name")).toHaveFocus();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(screen.getByLabelText("First name")).toHaveFocus();
  });

  it("offers Try again when the load fails", async () => {
    mine.mockRejectedValueOnce(new Error("offline"));
    mine.mockResolvedValueOnce({ household: null });
    render(<HouseholdCard />);
    await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No one in your household yet")).toBeInTheDocument();
  });

  it("formats birth dates and uses the violet badge only for Confirmed", async () => {
    mine.mockResolvedValue({
      household: { ...household, people: [{ ...household.people[0], linkStatus: "confirmed" }] },
    });
    render(<HouseholdCard />);
    expect(await screen.findByText("Born May 1, 2020")).toBeInTheDocument();
    expect(screen.getByText("Confirmed")).toHaveClass("bg-primary-subtle");
    expect(screen.getByText("Child")).toHaveClass("bg-sunken");
  });
});

describe("household name and address", () => {
  beforeEach(() => { vi.resetAllMocks(); mine.mockResolvedValue({ household: { ...household, people: [], address: "Old address" } }); });

  it("shows an empty household's details and saves through the existing endpoint", async () => {
    const { Households } = await import("../api");
    vi.mocked(Households.update).mockResolvedValue({ household: { ...household, people: [], name: "New household", address: "New address" } } as Awaited<ReturnType<typeof Households.update>>);
    render(<HouseholdCard />);
    const rename = await screen.findByRole("button", { name: "Rename" });
    expect(screen.getByText("Old address")).toBeInTheDocument();
    await userEvent.click(rename);
    expect(screen.getByLabelText(/^Household name/)).toHaveFocus();
    await userEvent.clear(screen.getByLabelText(/^Household name/)); await userEvent.type(screen.getByLabelText(/^Household name/), "New household");
    await userEvent.clear(screen.getByLabelText(/^Address/)); await userEvent.type(screen.getByLabelText(/^Address/), "New address");
    await userEvent.click(screen.getByRole("button", { name: "Save household" }));
    expect(Households.update).toHaveBeenCalledWith({ name: "New household", address: "New address" });
    expect(screen.getByText("New household")).toBeInTheDocument(); expect(screen.getByText("New address")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Saved your household name and address.");
    await vi.waitFor(() => expect(rename).toHaveFocus());
  });

  it("sends an explicit address clear and preserves valid fields on failure", async () => {
    const { Households } = await import("../api");
    vi.mocked(Households.update).mockRejectedValueOnce(new Error("offline"));
    render(<HouseholdCard />);
    await userEvent.click(await screen.findByRole("button", { name: "Edit address" }));
    expect(screen.getByLabelText(/^Household name/)).toHaveAttribute("maxlength", "100");
    expect(screen.getByLabelText(/^Address/)).toHaveAttribute("maxlength", "500");
    await userEvent.clear(screen.getByLabelText(/^Address/));
    await userEvent.click(screen.getByRole("button", { name: "Save household" }));
    expect(Households.update).toHaveBeenCalledWith({ name: "Smith household", address: "" });
    expect(screen.getByLabelText(/^Household name/)).toHaveValue("Smith household"); expect(screen.getByLabelText(/^Address/)).toHaveValue("");
    expect(screen.getByRole("alert")).toHaveTextContent("We couldn't reach Fold.");
  });

  it("validates a required name before sending", async () => {
    const { Households } = await import("../api"); render(<HouseholdCard />);
    await userEvent.click(await screen.findByRole("button", { name: "Rename" }));
    await userEvent.clear(screen.getByLabelText(/^Household name/));
    // Native validation and the shared field validation both reject it.
    screen.getByRole("form", { name: "Edit household" }).setAttribute("novalidate", "");
    await userEvent.click(screen.getByRole("button", { name: "Save household" }));
    expect(Households.update).not.toHaveBeenCalled(); expect(screen.getByLabelText(/^Household name/)).toHaveAttribute("aria-invalid", "true");
  });

  it("preserves inaccessible care notes while explicitly clearing a visible optional field", async () => {
    const { Households } = await import("../api");
    mine.mockResolvedValue({ household: { ...household, people: [{ ...household.people[0], careNotes: undefined }] } });
    vi.mocked(Households.updatePerson).mockRejectedValue(new Error("offline"));
    render(<HouseholdCard />); await userEvent.click(await screen.findByRole("button", { name: "Edit Kid Smith" }));
    await userEvent.clear(screen.getByLabelText(/^Last name/)); await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(Households.updatePerson).toHaveBeenCalledWith(5, expect.objectContaining({ lastName: "", careNotes: undefined }));
    expect(screen.getByLabelText(/^Care notes/)).toHaveAccessibleDescription(expect.stringContaining("24 hours"));
  });
});
