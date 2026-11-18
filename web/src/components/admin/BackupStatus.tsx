import type { ReactNode } from "react";
import { AdminBackup } from "../../api/admin";
import { errorText } from "../../lib/errors";
import { useLoad } from "../../lib/useLoad";
import { formatWhen, lowerLead } from "../../lib/when";
import type { AdminBackupStatus } from "../../types";
import { ErrorNotice } from "../ErrorNotice";
import { AlertCircleIcon, CheckIcon } from "../icons";
import { EmptyState } from "../ui/EmptyState";
import { Skeleton } from "../ui/Skeleton";

// Admin → System → Backups (M5.2). The server answers one of five states and
// never an error for a missing or damaged status file, so every state here has
// its own words. It loads on its own: a failed read never hides the tasks below.

function size(bytes: number): string {
  if (bytes < 1024) return `${bytes} ${bytes === 1 ? "byte" : "bytes"}`;
  const units = ["KB", "MB", "GB", "TB", "PB"];
  let n = bytes / 1024;
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return `${n >= 10 ? Math.round(n) : Math.round(n * 10) / 10} ${units[i]}`;
}

const when = (iso: string) => formatWhen(iso, undefined, undefined, true);

// A problem with an icon and words, so it never rests on color alone. Amber,
// not red: nothing here is an emergency, and an empty state is never red.
function Warning({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="space-y-1 rounded-md border border-warning-border bg-warning-subtle p-4 text-warning-on-subtle">
      <h3 className="flex items-start gap-2 font-semibold">
        <AlertCircleIcon className="mt-0.5 size-5 shrink-0" />
        <span>{title}</span>
      </h3>
      {children && <div className="space-y-1 text-sm">{children}</div>}
    </div>
  );
}

// What the last good backup holds. A detail the status file couldn't vouch for
// is left out, never shown as zero or as "off".
function Details({ s }: { s: AdminBackupStatus }) {
  const rows: [string, string][] = [];
  if (s.dumpBytes !== null) rows.push(["Database", size(s.dumpBytes)]);
  if (s.uploadsBytes !== null) rows.push(["Photos", size(s.uploadsBytes)]);
  if (s.offsite !== null) rows.push(["Offsite copy", s.offsite === "uploaded" ? "Sent to your offsite storage" : "Not set up"]);
  if (rows.length === 0) return null;
  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
      {rows.map(([label, value]) => (
        <div key={label} className="contents">
          <dt className="text-text-muted">{label}</dt>
          <dd>{value}</dd>
        </div>
      ))}
    </dl>
  );
}

// A newer try that failed, apart from the last success. A step this build
// doesn't know says only that the try didn't finish.
function Failure({ s }: { s: AdminBackupStatus }) {
  if (s.lastFailedAt === null) return null;
  const at = lowerLead(when(s.lastFailedAt));
  const kept = s.lastOkAt !== null ? " The last good backup is still the one above." : "";
  if (s.lastFailedStep === "config") {
    return <Warning title="The backup settings need attention"><p>The last try, {at}, couldn&rsquo;t start.{kept}</p></Warning>;
  }
  return <Warning title="The last backup try didn't finish"><p>It stopped {at}.{kept}</p></Warning>;
}

const commands: [string, string][] = [
  ["Check the status from a shell", "docker compose exec app /fold backup status"],
  ["Make a backup now", "docker compose exec backup backup.sh now"],
  ["See what the backup service is doing", "docker compose logs backup"],
];

function ForOperators({ watched }: { watched: boolean }) {
  return (
    <details className="text-sm">
      <summary className="block cursor-pointer py-3 font-semibold text-primary hover:underline">For whoever runs Fold</summary>
      {watched ? (
        <ul className="mt-2 space-y-3">
          {commands.map(([label, command]) => (
            <li key={command}>
              <p className="text-text-muted">{label}</p>
              <pre className="mt-1 whitespace-pre-wrap wrap-anywhere rounded-md bg-sunken p-3 font-mono text-xs">{command}</pre>
            </li>
          ))}
        </ul>
      ) : (
        <p className="mt-2 max-w-prose text-text-muted">
          Backups show here when Fold runs from the standard docker-compose.yml, which includes the backup service and
          points Fold at the folder it writes its status to (FOLD_BACKUP_DIR).
        </p>
      )}
    </details>
  );
}

function Body({ s }: { s: AdminBackupStatus }) {
  const last = s.lastOkAt;
  if (s.state === "unwatched") {
    return (
      <EmptyState
        title="Fold isn't watching backups here."
        body="Check with whoever runs Fold that backups are being made, and that you can restore from one."
      />
    );
  }
  if (s.state === "unreadable") {
    return (
      <Warning title="Fold can't read the backup status">
        <p>Backups may still be running. Ask whoever runs Fold to check them.</p>
      </Warning>
    );
  }
  if (last === null) {
    return (
      <Warning title="Backups are overdue">
        <p>No backup has finished yet. Ask whoever runs Fold to check that the backup service is running.</p>
      </Warning>
    );
  }
  if (s.state === "stale") {
    return (
      <>
        <Warning title="Backups are overdue">
          <p>The last backup finished {lowerLead(when(last))}. Fold makes one every night, so something may be stopping them. Ask whoever runs Fold to check.</p>
        </Warning>
        <Details s={s} />
      </>
    );
  }
  return (
    <>
      <p className="flex items-start gap-2">
        <CheckIcon className="mt-0.5 size-5 shrink-0 text-success" />
        <span>Last backup finished {lowerLead(when(last))}.</span>
      </p>
      <Details s={s} />
    </>
  );
}

export function BackupStatus() {
  const load = useLoad(() => AdminBackup.status(), [], { refreshOnFocus: true });
  const s = load.data;
  return (
    <section aria-labelledby="backup-heading" className="space-y-3">
      <h2 id="backup-heading" className="text-xl font-bold">Backups</h2>
      {load.error ? (
        <ErrorNotice message={errorText(load.error, "We couldn't load the backup status.")} onRetry={() => void load.retry()} />
      ) : !s ? (
        <Skeleton kind="lines" label="Loading backup status…" />
      ) : (
        <>
          <Body s={s} />
          <Failure s={s} />
          <ForOperators watched={s.state !== "unwatched"} />
        </>
      )}
    </section>
  );
}
