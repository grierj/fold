import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";

const churchAddress = vi.fn();
const setChurchAddress = vi.fn();
vi.mock("../../api", async (orig) => {
  const real = await orig<typeof import("../../api")>();
  return { ...real, Admin: { ...real.Admin, churchAddress: () => churchAddress(), setChurchAddress: (a: string) => setChurchAddress(a) } };
});

import { ApiError } from "../../api";
import { ChurchAddressSetting } from "./ChurchAddressSetting";

const view = () => render(<MemoryRouter><ChurchAddressSetting /></MemoryRouter>);

beforeEach(() => {
  churchAddress.mockReset().mockResolvedValue({ address: "" });
  setChurchAddress.mockReset().mockImplementation(async (a: string) => ({ address: a.trim() }));
});

it("reminds the admin while the address is empty, and saves it", async () => {
  view();
  expect(await screen.findByText(/Add your church's mailing address/)).toBeInTheDocument();
  const box = screen.getByRole("textbox", { name: /Church mailing address/ });
  const save = screen.getByRole("button", { name: "Save address" });
  expect(save).toBeDisabled();
  await userEvent.type(box, "Grace Church{enter}12 Main St");
  await userEvent.click(save);
  expect(setChurchAddress).toHaveBeenCalledWith("Grace Church\n12 Main St");
  expect(await screen.findByRole("status")).toHaveTextContent("Saved");
  expect(screen.queryByText(/Add your church's mailing address/)).not.toBeInTheDocument();
});

it("shows the saved address with no reminder", async () => {
  churchAddress.mockResolvedValue({ address: "Grace Church\nBoise" });
  view();
  expect(await screen.findByRole("textbox", { name: /Church mailing address/ })).toHaveValue("Grace Church\nBoise");
  expect(screen.queryByText(/Add your church's mailing address/)).not.toBeInTheDocument();
});

it("says how long an address may be when the server refuses it", async () => {
  setChurchAddress.mockRejectedValue(new ApiError(400, "server words", { code: "church_address_long" }));
  view();
  const box = await screen.findByRole("textbox", { name: /Church mailing address/ });
  await userEvent.type(box, "Too long");
  await userEvent.click(screen.getByRole("button", { name: "Save address" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Keep the address to 6 lines and 300 characters.");
  expect(screen.queryByText("server words")).not.toBeInTheDocument();
});
