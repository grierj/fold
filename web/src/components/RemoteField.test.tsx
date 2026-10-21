import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { RemoteField, remoteBody, remoteFrom, remoteProblem, type RemoteValue } from "./RemoteField";

describe("RemoteField", () => {
  it("shows the capacity only when remote attendance is on", async () => {
    const onChange = vi.fn();
    const { rerender } = render(
      <RemoteField idPrefix="t" value={{ available: false, capacity: "" }} onChange={onChange} />
    );
    expect(screen.getByLabelText(/^Remote attendance/)).not.toBeChecked();
    expect(screen.queryByLabelText(/^Remote capacity/)).not.toBeInTheDocument();

    await userEvent.click(screen.getByLabelText(/^Remote attendance/));
    expect(onChange).toHaveBeenCalledWith({ available: true, capacity: "" });

    rerender(<RemoteField idPrefix="t" value={{ available: true, capacity: "12" }} onChange={onChange} />);
    const cap = screen.getByLabelText(/^Remote capacity/) as HTMLInputElement;
    expect(cap.value).toBe("12");
    expect(screen.getByText(/Leave it blank for no limit/)).toBeInTheDocument();
  });

  it("maps stored values and the wire form both ways", () => {
    expect(remoteFrom(true, 30)).toEqual({ available: true, capacity: "30" });
    expect(remoteFrom(true, 0)).toEqual({ available: true, capacity: "0" });
    expect(remoteFrom(undefined, null)).toEqual({ available: false, capacity: "" });
    expect(remoteBody({ available: true, capacity: "" })).toEqual({ remoteAvailable: true, remoteCapacity: null });
    expect(remoteBody({ available: true, capacity: "0" })).toEqual({ remoteAvailable: true, remoteCapacity: 0 });
    expect(remoteBody({ available: false, capacity: "5" })).toEqual({ remoteAvailable: false, remoteCapacity: 5 });
  });

  it("rejects a cap that is not a whole number of 0 or more", () => {
    const bad: RemoteValue = { available: true, capacity: "-1" };
    expect(remoteProblem(bad)).toMatch(/whole number of 0 or more/);
    expect(remoteProblem({ available: true, capacity: "1.5" })).toMatch(/whole number/);
    expect(remoteProblem({ available: true, capacity: "" })).toBeNull();
    expect(remoteProblem({ available: true, capacity: "0" })).toBeNull();
  });
});
