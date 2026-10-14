import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("../api", () => ({ Site: { get: () => Promise.resolve({ name: "c", timezone: "America/Los_Angeles", selfSignup: "off" }) } }));
vi.mock("../lib/wall", () => ({ browserTimeZone: () => "America/Boise" }));

import { ZoneSelect } from "./ZoneSelect";

describe("ZoneSelect", () => {
  it("shows the browser zone first when the value is the church zone", async () => {
    render(<ZoneSelect id="tz" ariaLabel="Time zone" value="America/Los_Angeles" onChange={() => {}} />);
    const sel = screen.getByLabelText("Time zone") as HTMLSelectElement;
    await waitFor(() => expect(sel.options[1]?.textContent).toBe("Church · Los Angeles (Pacific)"));
    expect(sel.options[0].textContent).toBe("Yours · Boise (Mountain)");
    expect(sel.options[1].value).toBe("America/Los_Angeles");
    expect(sel.title).toBe("Church · Los Angeles (Pacific) · America/Los_Angeles");
    expect(sel.value).toBe("America/Los_Angeles");
  });
});

describe("ZoneSelect for the church setting", () => {
  it("lists the browser zone first, keeps the current value, and has no church entry", async () => {
    render(<ZoneSelect id="church-tz" ariaLabel="Church zone" forChurchSetting value="America/Los_Angeles" onChange={() => {}} />);
    const sel = screen.getByLabelText("Church zone") as HTMLSelectElement;
    await waitFor(() => expect(sel.options[0].textContent).toBe("Yours · Boise (Mountain)"));
    expect([...sel.options].some((o) => /church/i.test(o.textContent ?? ""))).toBe(false);
    expect([...sel.options].filter((o) => o.value === "America/Los_Angeles")).toHaveLength(1);
    expect(sel.value).toBe("America/Los_Angeles");
  });
});
