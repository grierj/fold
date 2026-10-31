import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { DONATION_WARNING, SmsComposer } from "./SmsComposer";

function Harness({ start = "" }: { start?: string }) {
  const [v, setV] = useState(start);
  return <SmsComposer church="Grace" value={v} onChange={setV} />;
}

describe("SmsComposer", () => {
  it("counts characters and texts, prefix and footer included", () => {
    render(<Harness />);
    const box = screen.getByRole("textbox", { name: "Text message" });
    expect(box).toHaveAccessibleDescription(/Grace: .*Reply STOP to opt out/);
    // "Grace: " (7) + "\n" + "Reply STOP to opt out" (21) = 29.
    expect(screen.getByRole("status")).toHaveTextContent("29 characters · 1 text · GSM-7, so each text holds 160");
    fireEvent.change(box, { target: { value: "x".repeat(132) } });
    expect(screen.getByRole("status")).toHaveTextContent("161 characters · 2 texts");
  });

  it("says when emoji make each text hold 70", () => {
    render(<Harness start="See you there 🎉" />);
    expect(screen.getByRole("status")).toHaveTextContent("Includes emoji or special characters, so each text holds 70");
  });

  it("warns about asking for money by text (DEC-3)", () => {
    render(<Harness start="Practice moved to 7" />);
    expect(screen.queryByText(DONATION_WARNING)).toBeNull();
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "Please give at tithe.ly/grace" } });
    expect(screen.getByText(DONATION_WARNING)).toBeInTheDocument();
  });
});
