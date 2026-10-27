import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { SeriesRsvpDialog } from "./SeriesRsvpDialog";

it("defaults to Just this date, traps focus, cancels with Escape and restores focus", async () => {
  const cancel = vi.fn();
  const trigger = document.createElement("button");
  trigger.textContent = "Going";
  document.body.append(trigger);
  trigger.focus();
  const view = render(<SeriesRsvpDialog answer="Going" onChoose={vi.fn()} onCancel={cancel} />);
  const first = screen.getByRole("button", { name: "Just this date" });
  expect(first).toHaveFocus();
  await userEvent.tab({ shift: true });
  expect(screen.getByRole("button", { name: "Cancel" })).toHaveFocus();
  await userEvent.tab();
  expect(first).toHaveFocus();
  await userEvent.keyboard("{Escape}");
  expect(cancel).toHaveBeenCalledOnce();
  view.unmount();
  expect(trigger).toHaveFocus();
  trigger.remove();
});

it("offers all three scopes with 44px option targets", async () => {
  const choose = vi.fn();
  render(<SeriesRsvpDialog answer="Maybe" onChoose={choose} onCancel={vi.fn()} />);
  for (const [name, scope] of [["Just this date", "just_this_date"], ["This and following dates", "this_and_following"], ["All future dates", "all_future"]] as const) {
    const option = screen.getByRole("button", { name });
    expect(option).toHaveClass("min-h-11");
    await userEvent.click(option);
    expect(choose).toHaveBeenLastCalledWith(scope);
  }
});
