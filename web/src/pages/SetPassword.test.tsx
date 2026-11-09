import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { AccountRequests } from "../api/accountRequests";
import { ApiError } from "../api/core";
import { SetPassword } from "./SetPassword";

const signedIn = vi.fn();
vi.mock("../auth", () => ({ useAuth: () => ({ signedIn }) }));
vi.mock("../api/accountRequests", () => ({ AccountRequests: { confirm: vi.fn() } }));

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/set-password" element={<SetPassword />} />
        <Route path="/" element={<p>Home page</p>} />
        <Route path="/waiting" element={<p>Waiting for consent</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

async function fillAndSubmit(password: string) {
  await userEvent.type(screen.getByLabelText("Password"), password);
  await userEvent.type(screen.getByLabelText("Confirm password"), password);
  await userEvent.click(screen.getByRole("button", { name: "Set password" }));
}

beforeEach(() => {
  vi.resetAllMocks();
  signedIn.mockResolvedValue("/");
});

it("sends a link with no token back to log in", () => {
  renderAt("/set-password");
  expect(screen.getByText("This link is missing its token.")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Back to log in" })).toHaveAttribute("href", "/login");
  expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
});

it("sends the token with the first password and goes where signedIn says (a teen waits for consent)", async () => {
  vi.mocked(AccountRequests.confirm).mockResolvedValue({ user: { id: 1 } } as never);
  signedIn.mockResolvedValue("/waiting");
  renderAt("/set-password?token=tok-1");
  await fillAndSubmit("a long enough password");
  expect(await screen.findByText("Waiting for consent")).toBeInTheDocument();
  expect(AccountRequests.confirm).toHaveBeenCalledWith("tok-1", "a long enough password");
});

it("tells the person to ask the church office when the link has expired", async () => {
  vi.mocked(AccountRequests.confirm).mockRejectedValue(new ApiError(410, "gone", { code: "link_expired" }));
  renderAt("/set-password?token=old");
  await fillAndSubmit("a long enough password");
  expect(await screen.findByText("This link has expired or was already used. Ask your church office for a new one.")).toBeInTheDocument();
  expect(signedIn).not.toHaveBeenCalled();
});
