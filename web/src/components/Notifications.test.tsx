import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Notification } from "../types";
import { NotificationBell } from "./Notifications";

const list = vi.fn();
vi.mock("../api", () => ({ Notifications: { list: () => list() } }));
vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 7 } }) }));

const notice = (id: string): Notification => ({
  id, type: "approval", title: "1 group needs approval", body: "", link: "/admin?tab=approvals",
});

function renderBell() {
  return render(
    <MemoryRouter>
      <NotificationBell />
    </MemoryRouter>
  );
}

describe("NotificationBell", () => {
  beforeEach(() => {
    list.mockReset();
    localStorage.clear();
  });

  it("refetches when the window regains focus", async () => {
    list.mockResolvedValue({ notifications: [] });
    renderBell();
    await act(async () => {});
    expect(list).toHaveBeenCalledTimes(1);
    list.mockResolvedValue({ notifications: [notice("approval:groups:5:1")] });
    await act(async () => {
      fireEvent.focus(window);
    });
    expect(list).toHaveBeenCalledTimes(2);
    expect(await screen.findByText("1")).toBeInTheDocument();
  });

  it("a dismissed notice doesn't hide a new one with the same wording", async () => {
    localStorage.setItem("fold-dismissed-notifications:7", JSON.stringify(["approval:groups:5:1"]));
    list.mockResolvedValue({ notifications: [notice("approval:groups:5:1")] });
    renderBell();
    await act(async () => {});
    expect(screen.queryByText("1")).toBeNull();

    list.mockResolvedValue({ notifications: [notice("approval:groups:9:1")] });
    await act(async () => {
      fireEvent.focus(window);
    });
    await userEvent.click(screen.getByRole("button", { name: "Notifications" }));
    expect(screen.getByText("1 group needs approval")).toBeInTheDocument();
  });
});
