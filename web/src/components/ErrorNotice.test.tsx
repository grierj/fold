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
});
