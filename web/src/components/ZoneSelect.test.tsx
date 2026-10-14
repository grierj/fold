import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("../api", () => ({ Site: { get: () => Promise.resolve({ name: "c", timezone: "America/Los_Angeles", selfSignup: "off" }) } }));
vi.mock("../lib/wall", () => ({ browserTimeZone: () => "America/Boise" }));

import { ZoneSelect } from "./ZoneSelect";

describe("ZoneSelect", () => {
  it("shows the browser zone first when the value is the church zone", async () => {
    render(<ZoneSelect id="tz" ariaLabel="Time zone" value="America/Los_Angeles" onChange={() => {}} />);
    const sel = screen.getByLabelText("Time zone") as HTMLSelectElement;
    await waitFor(() => expect(sel.options[1]?.textContent).toMatch(/Church time zone/));
    expect(sel.options[0].textContent).toBe("Your time zone: America/Boise");
    expect(sel.value).toBe("America/Los_Angeles");
  });
});
