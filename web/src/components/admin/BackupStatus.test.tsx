import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { AdminBackup } from "../../api/admin";
import { ApiError } from "../../api/core";
import type { AdminBackupStatus } from "../../types";
import { BackupStatus } from "./BackupStatus";

vi.mock("../../api/admin", async (actual) => ({ ...(await actual<typeof import("../../api/admin")>()), AdminBackup: { status: vi.fn() } }));

const status = (over: Partial<AdminBackupStatus> = {}): AdminBackupStatus => ({
  state: "ok", lastOkAt: "2026-11-12T10:31:00Z", dumpBytes: 1_572_864, uploadsBytes: 3_221_225_472, offsite: "uploaded",
  lastFailedAt: null, lastFailedStep: null, ...over,
});
const show = async (s: AdminBackupStatus) => {
  vi.mocked(AdminBackup.status).mockResolvedValue(s);
  render(<BackupStatus />);
  await screen.findByRole("heading", { name: "Backups" });
  await waitFor(() => expect(screen.queryByText("Loading backup status…")).not.toBeInTheDocument());
};
const row = (label: string) => screen.getByText(label).nextElementSibling;

beforeEach(() => { vi.mocked(AdminBackup.status).mockReset(); });

it("shows a skeleton under the heading while it loads", () => {
  vi.mocked(AdminBackup.status).mockReturnValue(new Promise(() => {}));
  render(<BackupStatus />);
  expect(screen.getByRole("heading", { name: "Backups", level: 2 })).toBeInTheDocument();
  expect(screen.getByText("Loading backup status…")).toBeInTheDocument();
});

it("says when the last backup finished and what it holds, with no warning", async () => {
  await show(status());
  expect(screen.getByText(/^Last backup finished .*Nov 1[12] at \d{1,2}:\d{2}.*\.$/)).toBeInTheDocument();
  expect(row("Database")).toHaveTextContent("1.5 MB");
  expect(row("Photos")).toHaveTextContent("3 GB");
  expect(row("Offsite copy")).toHaveTextContent("Sent to your offsite storage");
  expect(screen.queryByText("Backups are overdue")).not.toBeInTheDocument();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});

it("says offsite copies are not set up only when the status says off", async () => {
  await show(status({ offsite: "off" }));
  expect(row("Offsite copy")).toHaveTextContent("Not set up");
});

it("leaves out every detail the status file could not vouch for, never showing zero or off", async () => {
  await show(status({ dumpBytes: null, uploadsBytes: null, offsite: null }));
  expect(screen.getByText(/^Last backup finished/)).toBeInTheDocument();
  expect(screen.queryByText("Database")).not.toBeInTheDocument();
  expect(screen.queryByText("Photos")).not.toBeInTheDocument();
  expect(screen.queryByText("Offsite copy")).not.toBeInTheDocument();
  expect(document.body).not.toHaveTextContent(/\b0 bytes|Not set up/);
});

it("shows a real zero-byte size as bytes and keeps the other details independent", async () => {
  await show(status({ dumpBytes: null, uploadsBytes: 0, offsite: null }));
  expect(row("Photos")).toHaveTextContent("0 bytes");
  expect(screen.queryByText("Database")).not.toBeInTheDocument();
});

it("warns that backups are overdue with the last time, in words and an icon", async () => {
  await show(status({ state: "stale", lastOkAt: "2026-11-11T02:30:00Z" }));
  const title = screen.getByRole("heading", { name: "Backups are overdue", level: 3 });
  expect(title.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
  expect(title.parentElement).toHaveTextContent(/The last backup finished .*Nov 1[01] at \d{1,2}:\d{2}/);
  expect(title.parentElement).toHaveTextContent("Ask whoever runs Fold to check.");
  expect(screen.queryByText(/^Last backup finished/)).not.toBeInTheDocument();
  expect(row("Database")).toHaveTextContent("1.5 MB");
});

it("says no backup has finished yet when none ever did, without red or an alert", async () => {
  await show(status({ state: "missing", lastOkAt: null, dumpBytes: null, uploadsBytes: null, offsite: null }));
  const title = screen.getByRole("heading", { name: "Backups are overdue" });
  expect(title.parentElement).toHaveTextContent("No backup has finished yet.");
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  expect(document.querySelector(".text-danger-on-subtle, .bg-danger-subtle")).toBeNull();
});

it("says Fold can't read the status without claiming the backups are late", async () => {
  await show(status({ state: "unreadable", lastOkAt: null, dumpBytes: null, uploadsBytes: null, offsite: null }));
  expect(screen.getByRole("heading", { name: "Fold can't read the backup status" })).toBeInTheDocument();
  expect(screen.queryByText("Backups are overdue")).not.toBeInTheDocument();
});

it("gives neutral guidance when backups are not watched, with no warning and no commands", async () => {
  await show(status({ state: "unwatched", lastOkAt: null, dumpBytes: null, uploadsBytes: null, offsite: null }));
  expect(screen.getByText("Fold isn't watching backups here.")).toBeInTheDocument();
  expect(screen.getByText(/Check with whoever runs Fold/)).toBeInTheDocument();
  expect(screen.queryByText("Backups are overdue")).not.toBeInTheDocument();
  expect(screen.queryByRole("heading", { level: 3 })).not.toBeInTheDocument();
  expect(document.body).not.toHaveTextContent("docker compose exec");
  expect(document.body).toHaveTextContent("FOLD_BACKUP_DIR");
});

it("shows a newer failure apart from the last success", async () => {
  await show(status({ lastFailedAt: "2026-11-13T02:40:00Z", lastFailedStep: "upload" }));
  expect(screen.getByText(/^Last backup finished/)).toBeInTheDocument();
  const title = screen.getByRole("heading", { name: "The last backup try didn't finish", level: 3 });
  expect(title.parentElement).toHaveTextContent(/It stopped .*Nov 1[23] at \d{1,2}:\d{2}.*The last good backup is still the one above\./);
});

it("says the settings need attention when the failure was the configuration", async () => {
  await show(status({ state: "missing", lastOkAt: null, dumpBytes: null, uploadsBytes: null, offsite: null, lastFailedAt: "2026-11-13T02:40:00Z", lastFailedStep: "config" }));
  const title = screen.getByRole("heading", { name: "The backup settings need attention" });
  expect(title.parentElement).toHaveTextContent(/The last try, .*, couldn’t start\./);
  expect(title.parentElement).not.toHaveTextContent("still the one above");
  expect(screen.getByRole("heading", { name: "Backups are overdue" })).toBeInTheDocument();
});

it("words a failure at an unknown step as a try that didn't finish, and never invents a paused state", async () => {
  await show(status({ lastFailedAt: "2026-11-13T02:40:00Z", lastFailedStep: null }));
  expect(screen.getByRole("heading", { name: "The last backup try didn't finish" })).toBeInTheDocument();
  expect(document.body).not.toHaveTextContent(/paus|restor|recover|maintenance/i);
});

it("folds the shell commands for whoever runs Fold away", async () => {
  await show(status());
  const details = screen.getByText("For whoever runs Fold").closest("details")!;
  expect(details).not.toHaveAttribute("open");
  expect(within(details).getByText("docker compose exec app /fold backup status")).toBeInTheDocument();
  expect(within(details).getByText("docker compose exec backup backup.sh now")).toBeInTheDocument();
  expect(within(details).getByText("docker compose logs backup")).toBeInTheDocument();
});

it("says in plain words when it can't load, never with the server's text, and tries again", async () => {
  vi.mocked(AdminBackup.status).mockRejectedValueOnce(new ApiError(500, "pq: secret path /backup-status", { code: "server_error" }));
  vi.mocked(AdminBackup.status).mockResolvedValue(status());
  render(<BackupStatus />);
  expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't load the backup status. Please try again in a minute.");
  expect(document.body).not.toHaveTextContent(/pq:|\/backup-status/);
  expect(screen.queryByText(/^Last backup finished/)).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Try again" }));
  expect(await screen.findByText(/^Last backup finished/)).toBeInTheDocument();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});

it("uses the plain connection line when the request never reached Fold", async () => {
  vi.mocked(AdminBackup.status).mockRejectedValue(new TypeError("Failed to fetch"));
  render(<BackupStatus />);
  expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't reach Fold. Check your connection and try again.");
});

it("reads the status again when the window regains focus, so an old warning clears", async () => {
  vi.mocked(AdminBackup.status).mockResolvedValueOnce(status({ state: "stale", lastOkAt: "2026-11-10T02:30:00Z" }));
  render(<BackupStatus />);
  expect(await screen.findByText("Backups are overdue")).toBeInTheDocument();
  vi.mocked(AdminBackup.status).mockResolvedValue(status());
  window.dispatchEvent(new Event("focus"));
  expect(await screen.findByText(/^Last backup finished/)).toBeInTheDocument();
  expect(screen.queryByText("Backups are overdue")).not.toBeInTheDocument();
  cleanup();
});
