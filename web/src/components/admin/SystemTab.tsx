import { useEffect, useRef, useState } from "react";
import { AdminJobs } from "../../api";
import { errorText } from "../../lib/errors";
import { formatWhen } from "../../lib/when";
import type { AdminJob } from "../../types";
import { ErrorNotice } from "../ErrorNotice";
import { Button } from "../ui/Button";
import { EmptyState } from "../ui/EmptyState";
import { FormError } from "../ui/Field";
import { Skeleton } from "../ui/Skeleton";

const tries = (n: number) => `${n} ${n === 1 ? "try" : "tries"}`;

// One background task that kept failing: what it does, when it gave up,
// and Retry now or Dismiss. The error text is for whoever runs Fold, so it
// stays folded away.
function JobRow({ job, onDone }: { job: AdminJob; onDone: (id: number, message: string) => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // The button pressed, which takes the cursor back once a failed action
  // re-enables it.
  const [refocus, setRefocus] = useState<"retry" | "dismiss" | null>(null);
  const retryRef = useRef<HTMLButtonElement>(null);
  const dismissRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (busy || !refocus) return;
    (refocus === "retry" ? retryRef : dismissRef).current?.focus();
    setRefocus(null);
  }, [busy, refocus]);

  async function run(which: "retry" | "dismiss", action: () => Promise<unknown>, message: string, fallback: string) {
    setBusy(true);
    setError(null);
    try {
      await action();
      onDone(job.id, message);
    } catch (e) {
      setError(errorText(e, fallback));
      setRefocus(which);
      setBusy(false);
    }
  }

  // Names for a screen reader, which hears these buttons in a list of cards.
  const forLabel = <>{" "}<span className="sr-only">{job.label}</span></>;
  return (
    <li className="bg-surface rounded-lg border border-border p-6 space-y-3">
      <h3 className="font-bold wrap-anywhere">{job.label}</h3>
      <p className="text-sm text-text-muted">Gave up {formatWhen(job.failedAt)} after {tries(job.attempts)}.</p>
      <p className="text-sm">
        {job.runsDaily
          ? <>This runs again tomorrow by itself, so you don&rsquo;t have to do anything.</>
          : <>Retry now to try again, or dismiss it if you don&rsquo;t need it.</>}
      </p>
      {job.lastError && (
        <details className="text-sm">
          <summary className="block cursor-pointer py-3 font-semibold text-primary hover:underline">Show the error</summary>
          <pre className="mt-2 whitespace-pre-wrap wrap-anywhere font-mono text-xs bg-sunken rounded-md p-3">{job.lastError}</pre>
        </details>
      )}
      {error && <FormError>{error}</FormError>}
      <div className="flex flex-wrap gap-3">
        <Button ref={retryRef} variant="secondary" disabled={busy} onClick={() => void run("retry", () => AdminJobs.retry(job.id), "Queued to run again.", "We couldn't queue that task again.")}>
          Retry now{forLabel}
        </Button>
        <Button ref={dismissRef} variant="ghost" disabled={busy} onClick={() => void run("dismiss", () => AdminJobs.dismiss(job.id), "Dismissed. It won't show here again.", "We couldn't dismiss that task.")}>
          Dismiss{forLabel}
        </Button>
      </div>
    </li>
  );
}

// Admin → System (M3.1b, DEC-111): background tasks that ran out of
// attempts. Admins aren't told about them elsewhere until message sending
// brings failures they can fix (M3.5).
export function SystemTab({ jobs, more, error, reload, onRemoved }: {
  // Null until loaded.
  jobs: AdminJob[] | null;
  more: boolean;
  error: string | null;
  reload: () => void;
  // A job was retried or dismissed; the list drops it.
  onRemoved: (id: number) => void;
}) {
  // n makes a repeat of the same words still count as a new announcement.
  const [status, setStatus] = useState({ text: "", n: 0 });
  const statusRef = useRef<HTMLParagraphElement>(null);
  // The row is gone after an action, so the cursor moves to what happened.
  useEffect(() => {
    if (status.n > 0) statusRef.current?.focus();
  }, [status.n]);

  return (
    <section aria-labelledby="system-heading" className="space-y-3">
      <h2 id="system-heading" className="text-xl font-bold">Background tasks</h2>
      <p className="text-text-muted">
        Fold does some work in the background, like adding upcoming event dates. A task that keeps failing shows here.
      </p>
      <p key={status.n} ref={statusRef} role="status" tabIndex={-1} className={status.text ? "text-sm text-text-muted focus:outline-none" : "sr-only"}>{status.text}</p>
      {error && <ErrorNotice message={error} onRetry={reload} />}
      {!error && jobs === null && <Skeleton kind="lines" label="Loading background tasks…" />}
      {!error && jobs !== null && jobs.length === 0 && (
        <EmptyState title="Nothing needs attention." body="Background tasks are running normally." />
      )}
      {!error && jobs !== null && jobs.length > 0 && (
        <ul className="space-y-3">
          {jobs.map((j) => (
            <JobRow key={j.id} job={j} onDone={(id, message) => {
              setStatus((s) => ({ text: message, n: s.n + 1 }));
              onRemoved(id);
              // More failed than were listed: fetch the next ones.
              if (more) reload();
            }} />
          ))}
        </ul>
      )}
      {!error && more && <p className="text-sm text-text-muted">More tasks failed than fit here. Retry or dismiss these to see the rest.</p>}
    </section>
  );
}
