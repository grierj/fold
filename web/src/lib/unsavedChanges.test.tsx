import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { Link, Outlet, RouterProvider, createMemoryRouter, useNavigate } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { UnsavedChangesBlocker, useUnsavedChanges } from "./unsavedChanges";

function Editor({ label = "Edit" }: { label?: string }) {
  const [dirty, setDirty] = useState(false);
  const { guard, markSaved } = useUnsavedChanges(dirty);
  const navigate = useNavigate();
  return <>
    {guard}
    <button type="button" onClick={() => setDirty(true)}>{label}</button>
    <button type="button" onClick={() => { markSaved(); navigate("/done"); }}>Save</button>
    <Link to="/elsewhere">Elsewhere</Link>
  </>;
}

function renderAt(page = <Editor />) {
  const router = createMemoryRouter([{
    element: <><UnsavedChangesBlocker /><Outlet /></>,
    children: [
      { path: "/", element: page },
      { path: "/elsewhere", element: <p>Elsewhere page</p> },
      { path: "/done", element: <p>Saved page</p> },
    ],
  }]);
  render(<RouterProvider router={router} />);
  return router;
}

afterEach(() => vi.restoreAllMocks());

it("lets a clean form go without asking", async () => {
  const confirm = vi.spyOn(window, "confirm");
  renderAt();
  await userEvent.click(screen.getByRole("link", { name: "Elsewhere" }));
  expect(screen.getByText("Elsewhere page")).toBeInTheDocument();
  expect(confirm).not.toHaveBeenCalled();
});

it("asks before leaving unsaved edits, and stays when told to", async () => {
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  renderAt();
  await userEvent.click(screen.getByRole("button", { name: "Edit" }));
  await userEvent.click(screen.getByRole("link", { name: "Elsewhere" }));
  expect(confirm).toHaveBeenCalledWith("Leave without saving? Your changes will be lost.");
  expect(screen.queryByText("Elsewhere page")).not.toBeInTheDocument();
  confirm.mockReturnValue(true);
  await userEvent.click(screen.getByRole("link", { name: "Elsewhere" }));
  expect(screen.getByText("Elsewhere page")).toBeInTheDocument();
});

it("lets a save navigate away without asking", async () => {
  const confirm = vi.spyOn(window, "confirm");
  renderAt();
  await userEvent.click(screen.getByRole("button", { name: "Edit" }));
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  expect(screen.getByText("Saved page")).toBeInTheDocument();
  expect(confirm).not.toHaveBeenCalled();
});

it("asks the browser before closing the tab with unsaved edits", async () => {
  renderAt();
  const clean = new Event("beforeunload", { cancelable: true });
  act(() => { window.dispatchEvent(clean); });
  expect(clean.defaultPrevented).toBe(false);
  await userEvent.click(screen.getByRole("button", { name: "Edit" }));
  const dirty = new Event("beforeunload", { cancelable: true });
  act(() => { window.dispatchEvent(dirty); });
  expect(dirty.defaultPrevented).toBe(true);
});

it("guards every open form, not only the last one mounted", async () => {
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  renderAt(<><Editor label="Edit first" /><Editor label="Edit second" /></>);
  await userEvent.click(screen.getByRole("button", { name: "Edit first" }));
  await userEvent.click(screen.getAllByRole("link", { name: "Elsewhere" })[0]);
  expect(confirm).toHaveBeenCalled();
  expect(screen.queryByText("Elsewhere page")).not.toBeInTheDocument();
});
