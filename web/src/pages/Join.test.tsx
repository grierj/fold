import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { Join } from "./Join";

import { ApiError } from "../api/core";
import { Site } from "../api/site";

vi.mock("../api/site", () => ({ Site: { get: vi.fn() } }));
beforeEach(() => vi.mocked(Site.get).mockResolvedValue({ name: "Grace", selfSignup: "off" } as never));

it("keeps the login link phone-sized without enlarging the surrounding prose", async () => {
  render(<MemoryRouter><Join /></MemoryRouter>);
  await screen.findByRole("heading", { name: "Request an account at Grace" });
  expect(screen.getByText("Send your request.")).toBeInTheDocument();
  expect(screen.getByText("An admin reviews it.")).toBeInTheDocument();
  expect(screen.getByText("We email you a link to set your password.")).toBeInTheDocument();
  const link = screen.getByRole("link", { name: "Log in" });
  expect(link).toHaveAttribute("href", "/login");
  expect(link).toHaveClass("text-link");
  expect(link.parentElement?.className).not.toMatch(/leading-/);
});

it("says so, with a retry, and shows no form when the church's signup setting can't be read", async () => {
  vi.mocked(Site.get).mockRejectedValueOnce(new ApiError(500, "x")).mockResolvedValue({ name: "Grace", selfSignup: "request" } as never);
  render(<MemoryRouter><Join /></MemoryRouter>);
  expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't check whether this church takes account requests.");
  expect(screen.queryByRole("button", { name: "Request an account" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Try again" }));
  expect(await screen.findByRole("button", { name: "Request an account" })).toBeInTheDocument();
});

it("tells people self-signup is off and shows no form", async () => {
  render(<MemoryRouter><Join /></MemoryRouter>);
  expect(await screen.findByText(/Self-signup is turned off for this church/)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Request an account" })).not.toBeInTheDocument();
});
