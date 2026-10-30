import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { RemoteField, focusRemoteCapacity, remoteBody, remoteFrom, remoteProblem, type RemoteValue } from "./RemoteField";

describe("RemoteField", () => {
  it("shows the capacity only when remote attendance is on", async () => {
    const onChange = vi.fn();
    const { rerender } = render(
      <RemoteField idPrefix="t" value={{ available: false, capacity: "" }} onChange={onChange} />
    );
    expect(screen.getByLabelText(/^Remote attendance/)).not.toBeChecked();
    expect(screen.queryByLabelText(/^Remote seat limit/)).not.toBeInTheDocument();

    await userEvent.click(screen.getByLabelText(/^Remote attendance/));
    expect(onChange).toHaveBeenCalledWith({ available: true, capacity: "" });

    rerender(<RemoteField idPrefix="t" value={{ available: true, capacity: "12" }} onChange={onChange} />);
    const cap = screen.getByLabelText(/^Remote seat limit/) as HTMLInputElement;
    expect(cap.value).toBe("12");
    expect(screen.getByText(/Leave it blank for no limit/)).toBeInTheDocument();
  });

  it("maps stored values and the wire form both ways", () => {
    expect(remoteFrom(true, 30)).toEqual({ available: true, capacity: "30" });
    expect(remoteFrom(true, 0)).toEqual({ available: true, capacity: "0" });
    expect(remoteFrom(undefined, null)).toEqual({ available: false, capacity: "" });
    expect(remoteBody({ available: true, capacity: "" })).toEqual({ remoteAvailable: true, remoteCapacity: null });
    expect(remoteBody({ available: true, capacity: "3" })).toEqual({ remoteAvailable: true, remoteCapacity: 3 });
    expect(remoteBody({ available: false, capacity: "5" })).toEqual({ remoteAvailable: false, remoteCapacity: null });
  });

  it("rejects a cap that is not a positive whole number", () => {
    const bad: RemoteValue = { available: true, capacity: "-1" };
    expect(remoteProblem(bad)).toMatch(/whole number from 1/);
    expect(remoteProblem({ available: true, capacity: "1.5" })).toMatch(/whole number/);
    expect(remoteProblem({ available: true, capacity: "" })).toBeNull();
    expect(remoteProblem({ available: true, capacity: "0" })).toMatch(/Uncheck remote attendance/);
  });

  it.each(["1e", "-2", "1.5", "100001", "99999999999999999999"])("rejects invalid typed text %s", (capacity) => {
    expect(remoteProblem({ available: true, capacity })).toMatch(/whole number/);
    expect(remoteProblem({ available: true, capacity }, true)).toMatch(/whole number/);
  });

  it("allows zero only on a date, and ignores a disabled pool's old limit", () => {
    expect(remoteProblem({ available: true, capacity: "0" }, true)).toBeNull();
    expect(remoteProblem({ available: true, capacity: " 12 " })).toBeNull();
    expect(remoteProblem({ available: false, capacity: "0" })).toBeNull();
  });

  it("keeps invalid text, wires its Fold error and focuses the field", async () => {
    const error = remoteProblem({ available: true, capacity: "1e" });
    render(<RemoteField idPrefix="bad" value={{ available: true, capacity: "1e" }} onChange={() => {}} error={error} />);
    const input = screen.getByRole("textbox", { name: /^Remote seat limit/ });
    expect(input).toHaveValue("1e");
    expect(input).toHaveAttribute("inputmode", "numeric");
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(input).toHaveAccessibleDescription(expect.stringContaining(error!));
    expect(screen.getByRole("checkbox")).toHaveAccessibleDescription("Add a video link above so remote people can join.");
    focusRemoteCapacity("bad");
    await waitFor(() => expect(input).toHaveFocus());
  });
});
