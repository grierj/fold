import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeEach, expect, it, vi } from "vitest";
import { AdminJobs, ApiError } from "../../api";
import { ERROR_COPY } from "../../lib/errors";
import type { AdminJob } from "../../types";
import { SystemTab } from "./SystemTab";

vi.mock("../../api", async (actual) => ({
  ...(await actual<typeof import("../../api")>()),
  AdminJobs: { list: vi.fn(), retry: vi.fn(), dismiss: vi.fn() },
}));

const job = (id: number, extra: Partial<AdminJob> = {}): AdminJob => ({
  id, kind: "occurrences.extend", label: "Adding upcoming event dates", runsDaily: true, attempts: 5,
  lastError: "pq: connection refused", failedAt: "2026-10-30T13:00:00Z", ...extra,
});

// The tab with the list state its parent (the Admin page) keeps.
function Harness({ initial, more = false, error = null, reload = () => {} }: { initial: AdminJob[] | null; more?: boolean; error?: string | null; reload?: () => void }) {
  const [jobs, setJobs] = useState(initial);
  return <SystemTab jobs={jobs} more={more} error={error} reload={reload} onRemoved={(id) => setJobs((p) => p && p.filter((j) => j.id !== id))} />;
}

beforeEach(() => {
  vi.mocked(AdminJobs.retry).mockReset().mockResolvedValue({ status: "queued" });
  vi.mocked(AdminJobs.dismiss).mockReset().mockResolvedValue({ status: "dismissed" });
});

it("says nothing needs attention when no task failed, never in red", () => {
  render(<Harness initial={[]} />);
  expect(screen.getByRole("heading", { name: "Background tasks" })).toBeInTheDocument();
  expect(screen.getByText("Nothing needs attention.")).toBeInTheDocument();
  expect(screen.getByText("Background tasks are running normally.")).toBeInTheDocument();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});

it("shows each failed task in plain words, with the error folded away", () => {
  render(<Harness initial={[job(1), job(2, { kind: "message.send", label: "Sending a message", runsDaily: false, attempts: 1 })]} more />);
  expect(screen.getByRole("heading", { name: "Adding upcoming event dates" })).toBeInTheDocument();
  expect(screen.getByText(/^Gave up .* after 5 tries\.$/)).toBeInTheDocument();
  expect(screen.getByText(/^Gave up .* after 1 try\.$/)).toBeInTheDocument();
  // Only the daily one reassures that it runs again by itself.
  expect(screen.getAllByText(/runs again tomorrow by itself/)).toHaveLength(1);
  expect(screen.getByText(/^Retry now to try again, or dismiss it if you don.t need it\.$/)).toBeInTheDocument();
  const details = screen.getAllByText("Show the error")[0].closest("details")!;
  expect(details).not.toHaveAttribute("open");
  expect(details).toHaveTextContent("pq: connection refused");
  expect(screen.getByRole("button", { name: "Retry now Sending a message" })).toBeInTheDocument();
  expect(screen.getByText(/More tasks failed than fit here/)).toBeInTheDocument();
});

it("retries or dismisses a task, drops its row and says what happened", async () => {
  render(<Harness initial={[job(1), job(2, { label: "Clearing old alerts" })]} />);
  await userEvent.click(screen.getByRole("button", { name: "Retry now Adding upcoming event dates" }));
  expect(AdminJobs.retry).toHaveBeenCalledWith(1);
  expect(await screen.findByRole("status")).toHaveTextContent("Queued to run again.");
  expect(screen.queryByRole("heading", { name: "Adding upcoming event dates" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Dismiss Clearing old alerts" }));
  expect(AdminJobs.dismiss).toHaveBeenCalledWith(2);
  await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Dismissed. It won't show here again."));
  expect(screen.getByText("Nothing needs attention.")).toBeInTheDocument();
});

it("keeps the row and says so when someone else already handled the task", async () => {
  vi.mocked(AdminJobs.retry).mockRejectedValue(new ApiError(409, "that task was already handled; refresh the list", { code: "job_handled" }));
  render(<Harness initial={[job(1)]} />);
  await userEvent.click(screen.getByRole("button", { name: "Retry now Adding upcoming event dates" }));
  expect(await screen.findByText(ERROR_COPY.job_handled)).toBeInTheDocument();
  expect(screen.getByRole("heading", { name: "Adding upcoming event dates" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Retry now Adding upcoming event dates" })).toBeEnabled();
});

it("never makes a failed load look like nothing needs attention", async () => {
  const reload = vi.fn();
  render(<Harness initial={null} error="We couldn't load the background tasks." reload={reload} />);
  expect(screen.getByText("We couldn't load the background tasks.")).toBeInTheDocument();
  expect(screen.queryByText("Nothing needs attention.")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: /try again/i }));
  expect(reload).toHaveBeenCalled();
});

it("shows a skeleton while loading", () => {
  render(<Harness initial={null} />);
  expect(screen.getByText("Loading background tasks…")).toBeInTheDocument();
});

it("gives the cursor back to the pressed button when an action fails, and announces why", async () => {
  vi.mocked(AdminJobs.dismiss).mockRejectedValue(new ApiError(500, "x"));
  render(<Harness initial={[job(1)]} />);
  const dismiss = screen.getByRole("button", { name: "Dismiss Adding upcoming event dates" });
  await userEvent.click(dismiss);
  expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't dismiss that task.");
  await waitFor(() => expect(dismiss).toHaveFocus());
});

it("fetches the next tasks after an action when more failed than were listed", async () => {
  const reload = vi.fn();
  render(<Harness initial={[job(1), job(2)]} more reload={reload} />);
  await userEvent.click(screen.getAllByRole("button", { name: "Retry now Adding upcoming event dates" })[0]);
  await waitFor(() => expect(reload).toHaveBeenCalledTimes(1));
});
