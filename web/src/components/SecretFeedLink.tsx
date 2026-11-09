import { useEffect, useRef, useState, type DependencyList, type ReactNode } from "react";
import { errorText } from "../lib/errors";
import { formatEventDay } from "../lib/when";
import { useLoad } from "../lib/useLoad";
import { Button } from "./ui/Button";
import { Field, FormError, Input } from "./ui/Field";
import { Skeleton } from "./ui/Skeleton";
import { ErrorNotice } from "./ErrorNotice";

// A secret calendar link (the personal feed and a room's feed): the link shows
// once, a new link stops the old one, and a failed load never looks like "no
// link yet" (Create would then replace a link that works).
export interface FeedInfo {
  hasToken: boolean;
  createdAt?: string | null;
  // Present only in the answer that made the link.
  url?: string | null;
}

export function useSecretFeed<T extends FeedInfo>({ load, deps, create, revoke, failed, onInfo, onRevoked }: {
  load: () => Promise<T>;
  deps: DependencyList;
  create: () => Promise<T>;
  revoke: () => Promise<void>;
  failed: { load: string; create: string; revoke: string };
  // Called with each answer that carries the feed's state (a load or a new link).
  onInfo?: (info: T) => void;
  onRevoked?: () => void;
}) {
  const feed = useLoad(load, deps);
  const [hasToken, setHasToken] = useState(false);
  const [createdAt, setCreatedAt] = useState<string | null>(null);
  const [link, setLink] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState("");
  const linkRef = useRef<HTMLInputElement>(null);

  // Each answer fills the state in the render that first sees it.
  const [applied, setApplied] = useState(feed.data);
  if (feed.data && feed.data !== applied) {
    setApplied(feed.data);
    setHasToken(feed.data.hasToken);
    setCreatedAt(feed.data.createdAt ?? null);
    onInfo?.(feed.data);
  }

  // Runs one change with the busy flag, the error line and the status line.
  async function act(run: () => Promise<void>, failure: string) {
    setBusy(true);
    setError(null);
    setStatus("");
    try {
      await run();
    } catch (err) {
      setError(errorText(err, failure));
    } finally {
      setBusy(false);
    }
  }

  const make = () => act(async () => {
    const info = await create();
    setHasToken(true);
    setCreatedAt(info.createdAt ?? null);
    setLink(info.url ?? null);
    onInfo?.(info);
  }, failed.create);

  // A new link appears in the box: put focus there so it can be copied.
  useEffect(() => {
    if (link) linkRef.current?.focus();
  }, [link]);

  return {
    loaded: feed.data !== undefined,
    loadError: feed.error ? errorText(feed.error, failed.load) : null,
    retry: feed.retry,
    hasToken, createdAt, link, busy, error, status, linkRef,
    act, setError, setStatus,
    create: make,
    remake: (confirmText: string) => confirm(confirmText) ? make() : Promise.resolve(),
    turnOff: (confirmText: string) => confirm(confirmText) ? act(async () => {
      await revoke();
      setHasToken(false);
      setCreatedAt(null);
      setLink(null);
      setStatus("Calendar link turned off.");
      onRevoked?.();
    }, failed.revoke) : Promise.resolve(),
    async copyLink() {
      if (!link) return;
      setError(null);
      if (navigator.clipboard) {
        try {
          await navigator.clipboard.writeText(link);
          setStatus("Link copied.");
          return;
        } catch {
          // Fall through to selecting the text by hand.
        }
      }
      linkRef.current?.select();
      setStatus("The link is selected. Copy it from the box above.");
    },
  };
}

export type SecretFeed = ReturnType<typeof useSecretFeed>;

export function SecretFeedLink({ feed, id, loadingLabel, linkLabel, linkHint, setUp, none, confirmRemake, confirmOff, extras, after }: {
  feed: SecretFeed;
  id: string;
  loadingLabel: string;
  linkLabel: string;
  linkHint: string;
  // Said when a link exists but cannot be shown again.
  setUp: (madeOn: string | null) => ReactNode;
  none: ReactNode;
  confirmRemake: string;
  confirmOff: string;
  // Controls between the error line and the link (the personal feed's scope).
  extras?: ReactNode;
  // Rendered below the buttons, above the status line.
  after?: ReactNode;
}) {
  return <>
    {feed.loadError ? (
      <ErrorNotice message={feed.loadError} onRetry={feed.retry} />
    ) : !feed.loaded ? (
      <Skeleton kind="lines" count={2} label={loadingLabel} />
    ) : (
      <>
        {feed.error && <FormError>{feed.error}</FormError>}
        {extras}
        {feed.link ? (
          <Field id={id} label={linkLabel} hint={linkHint}>
            <Input ref={feed.linkRef} value={feed.link} readOnly layout="font-mono text-sm" onFocus={(e) => e.currentTarget.select()} />
          </Field>
        ) : feed.hasToken ? (
          <p className="text-sm text-text-muted">{setUp(feed.createdAt ? formatEventDay(feed.createdAt) : null)}</p>
        ) : (
          <p className="text-sm text-text-muted">{none}</p>
        )}
        <div className="flex flex-wrap gap-2">
          {feed.hasToken ? (
            <>
              {feed.link && (
                <Button variant="primary" layout="w-full sm:w-auto" onClick={() => void feed.copyLink()}>
                  Copy link
                </Button>
              )}
              <Button variant="secondary" layout="w-full sm:w-auto" onClick={() => void feed.remake(confirmRemake)} disabled={feed.busy}>
                {feed.busy ? "Saving…" : "Make a new link"}
              </Button>
              <Button variant="danger-outline" layout="w-full sm:w-auto" onClick={() => void feed.turnOff(confirmOff)} disabled={feed.busy}>
                Turn off calendar link
              </Button>
            </>
          ) : (
            <Button variant="primary" layout="w-full sm:w-auto" onClick={() => void feed.create()} disabled={feed.busy}>
              {feed.busy ? "Saving…" : "Create calendar link"}
            </Button>
          )}
        </div>
      </>
    )}
    {after}
    <p role="status" className="text-success text-sm empty:hidden">{feed.status}</p>
  </>;
}
