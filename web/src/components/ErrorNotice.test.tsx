import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ErrorNotice } from "./ErrorNotice";

describe("ErrorNotice", () => {
  it("announces the message and offers to try again", async () => {
    const retry = vi.fn();
    render(<ErrorNotice message="We couldn't load the groups." onRetry={retry} />);
    expect(screen.getByRole("alert")).toHaveTextContent("We couldn't load the groups.");
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(retry).toHaveBeenCalled();
  });
  it("uses the neutral card with an illustration when it can retry", () => {
    const { container } = render(<ErrorNotice message="We couldn't load the groups." onRetry={() => {}} />);
    expect(screen.getByRole("alert")).toHaveClass("bg-surface");
    expect(screen.getByRole("alert")).not.toHaveClass("bg-danger-subtle");
    expect(container.querySelector("img")).not.toBeNull();
  });
  it("stays the inline notice without a retry", () => {
    render(<ErrorNotice message="That didn't work." />);
    expect(screen.getByRole("alert")).toHaveClass("bg-danger-subtle");
    expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
  });
});
