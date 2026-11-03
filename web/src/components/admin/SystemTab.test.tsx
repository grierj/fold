import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeEach, expect, it, vi } from "vitest";
import { AdminJobs, ApiError } from "../../api";
import { ERROR_COPY } from "../../lib/errors";
import type { AdminDeliverySummary, AdminJob } from "../../types";
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

it("refreshes from the server after an action, even when nothing more was listed", async () => {
  const reload = vi.fn();
  render(<Harness initial={[job(1), job(2)]} reload={reload} />);
  await userEvent.click(screen.getAllByRole("button", { name: "Retry now Adding upcoming event dates" })[0]);
  await waitFor(() => expect(reload).toHaveBeenCalledTimes(1));
});

const summary = (extra: Partial<AdminDeliverySummary> = {}): AdminDeliverySummary => ({ queued: 0, deferred: 0, failed: 0, oldestQueuedAt: null, ...extra });

const deliverJob = (id: number) => job(id, { kind: "notify.deliver", label: "Sending a notice by email and to devices", runsDaily: false });
const renderTab = (deliveries: AdminDeliverySummary | null, jobs: AdminJob[] | null = [], extra: { more?: boolean; error?: string | null } = {}) =>
  render(<SystemTab jobs={jobs} more={extra.more ?? false} error={extra.error ?? null} reload={() => {}} onRemoved={() => {}} deliveries={deliveries} />);

it("says nothing about email when every delivery count is zero", () => {
  renderTab(summary());
  expect(document.body).not.toHaveTextContent(/email/i);
  expect(screen.getByText("Nothing needs attention.")).toBeInTheDocument();
});

it("says waiting emails send on their own, with the oldest time, and offers no Retry", () => {
  renderTab(summary({ queued: 3, oldestQueuedAt: "2026-10-30T15:14:00Z" }));
  expect(screen.getByText(/^3 emails are waiting to send\. The oldest has waited since .*\d{1,2}:\d{2}.*\. Fold sends them on its own\.$/)).toBeInTheDocument();
  expect(screen.queryByText("Nothing needs attention.")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /retry/i })).not.toBeInTheDocument();
});

it("words one waiting email without an oldest time", () => {
  renderTab(summary({ queued: 1 }));
  expect(screen.getByText("1 email is waiting to send. Fold sends it on its own.")).toBeInTheDocument();
});

it("points a stopped email at the listed notify.deliver task", () => {
  renderTab(summary({ deferred: 1 }), [deliverJob(5)]);
  expect(screen.getByText("1 email stopped after too many tries. To send it, use Retry now on “Sending a notice by email and to devices” below.")).toBeInTheDocument();
});

it("says stopped emails are being tried again when no task is listed", () => {
  renderTab(summary({ deferred: 2 }), [], { more: false });
  expect(screen.getByText("2 emails are being tried again.")).toBeInTheDocument();
  expect(document.body).not.toHaveTextContent(/below/);
  expect(screen.queryByText("Nothing needs attention.")).not.toBeInTheDocument();
});

it("says the task will show later when more tasks failed than are listed", () => {
  renderTab(summary({ deferred: 1 }), [job(1)], { more: true });
  expect(screen.getByText("1 email stopped after too many tries. Its task will show below once you retry or dismiss the tasks listed.")).toBeInTheDocument();
});

it("says refused emails could not be sent, with no recipient or error text", () => {
  renderTab(summary({ failed: 2 }));
  expect(screen.getByText("2 emails could not be sent in the last 7 days. The mail service refused them, or email isn't set up.")).toBeInTheDocument();
  expect(screen.queryByText("Nothing needs attention.")).not.toBeInTheDocument();
  expect(document.body).not.toHaveTextContent(/@|pq:|smtp/i);
});

it("joins queued, stopped and refused emails into one paragraph", () => {
  renderTab(summary({ queued: 1, deferred: 2, failed: 1 }), [deliverJob(5)]);
  expect(screen.getByText(
    "1 email is waiting to send. Fold sends it on its own. " +
    "2 emails stopped after too many tries. To send them, use Retry now on each “Sending a notice by email and to devices” task below. " +
    "1 email could not be sent in the last 7 days. The mail service refused it, or email isn't set up.",
  )).toBeInTheDocument();
});

it("shows no email counts beside a load error", () => {
  renderTab(summary({ queued: 3, failed: 2 }), null, { error: "We couldn't load the background tasks." });
  expect(document.body).not.toHaveTextContent(/emails? (is|are|could)/);
  expect(screen.queryByText("Nothing needs attention.")).not.toBeInTheDocument();
});

it("announces changed email counts politely in the same element", () => {
  const { rerender } = renderTab(summary({ deferred: 1 }), [deliverJob(5)]);
  const line = screen.getByText(/^1 email stopped/);
  expect(line).toHaveAttribute("aria-live", "polite");
  rerender(<SystemTab jobs={[]} more={false} error={null} reload={() => {}} onRemoved={() => {}} deliveries={summary({ queued: 1 })} />);
  expect(line).toHaveTextContent("1 email is waiting to send. Fold sends it on its own.");
});
