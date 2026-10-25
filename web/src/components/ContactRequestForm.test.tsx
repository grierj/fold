import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { ApiError } from "../api";
import { Contacts } from "../contact-api";
import { ContactRequestForm } from "./ContactRequestForm";

vi.mock("../contact-api", () => ({ Contacts: { create: vi.fn() } }));
beforeEach(() => vi.clearAllMocks());
const props = { subjectUserId: 2, subjectName: "Pat", contextType: "group" as const, contextId: 3, onClose: vi.fn() };
const mount = () => render(<MemoryRouter><ContactRequestForm {...props} /></MemoryRouter>);

it("creates an adult request with the exact context and optional reason, without revealing", async () => {
  vi.mocked(Contacts.create).mockResolvedValue({ request: { id: 12 } as never });
  mount();
  await userEvent.click(screen.getByLabelText("Email and phone"));
  await userEvent.type(screen.getByLabelText(/Reason/), "To coordinate rides");
  await userEvent.click(screen.getByRole("button", { name: "Send request" }));
  expect(Contacts.create).toHaveBeenCalledWith({ subjectUserId: 2, contextType: "group", contextId: 3, fields: "both", reason: "To coordinate rides" });
  expect(await screen.findByRole("link", { name: "View request" })).toHaveAttribute("href", "/contact-requests/12");
  expect(screen.queryByText(/Email:/)).not.toBeInTheDocument();
});

it("preserves field selection and reason on server failure and shows friendly error copy", async () => {
  vi.mocked(Contacts.create).mockRejectedValue(new ApiError(409, "SECRET", { code: "contact_open_limit" }));
  mount();
  await userEvent.click(screen.getByLabelText("Phone"));
  await userEvent.type(screen.getByLabelText(/Reason/), "Please call about transport");
  await userEvent.click(screen.getByRole("button", { name: "Send request" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("You have 10 open contact requests");
  expect(screen.getByLabelText("Phone")).toBeChecked();
  expect(screen.getByLabelText(/Reason/)).toHaveValue("Please call about transport");
  expect(screen.queryByText("SECRET")).not.toBeInTheDocument();
});

it("counts Unicode code points, allows 1000 astral characters, and focuses an overlong reason", async () => {
  vi.mocked(Contacts.create).mockRejectedValue(new ApiError(500, "oops"));
  mount();
  const reason = screen.getByLabelText(/Reason/);
  await userEvent.click(reason);
  await userEvent.paste("😀".repeat(1000));
  await userEvent.click(screen.getByRole("button", { name: "Send request" }));
  expect(Contacts.create).toHaveBeenCalledTimes(1);
  await userEvent.click(reason);
  await userEvent.paste("😀");
  await userEvent.click(screen.getByRole("button", { name: "Send request" }));
  expect(Contacts.create).toHaveBeenCalledTimes(1);
  expect(reason).toHaveAttribute("aria-invalid", "true");
  expect(reason).toHaveFocus();
});
