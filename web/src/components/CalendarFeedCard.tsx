import { useCallback, useEffect, useRef, useState } from "react";
import { CalendarFeeds } from "../api";
import type { CalendarFeed } from "../types";
import { errorText } from "../lib/errors";
import { Button } from "./ui/Button";
import { Choices, Field, FormError, Input, Radio } from "./ui/Field";
import { Skeleton } from "./ui/Skeleton";
import { ErrorNotice } from "./ErrorNotice";

export function CalendarFeedCard({ showHeading = true }: { showHeading?: boolean }) {
  const [loaded, setLoaded] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [hasToken, setHasToken] = useState(false);
  const [scope, setScope] = useState<CalendarFeed["scope"]>("mine");
  const [link, setLink] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState("");
  const linkRef = useRef<HTMLInputElement>(null);
  const alive = useRef(true);

  const load = useCallback(() => {
    setLoaded(false);
    setLoadError(null);
    CalendarFeeds.get()
      .then((r) => {
        if (!alive.current) return;
        setHasToken(r.calendarFeed.hasToken);
        setScope(r.calendarFeed.scope);
        setLoaded(true);
      })
      .catch((err) => {
        // A failed load must never look like "no link yet": Create would
        // then quietly replace a link that works (M17-REVIEW M1).
        if (alive.current) setLoadError(errorText(err, "We couldn't load your calendar link."));
      });
  }, []);

  useEffect(() => {
    alive.current = true;
    load();
    return () => {
      alive.current = false;
    };
  }, [load]);

  async function create() {
    setBusy(true);
    setError(null);
    setStatus("");
    try {
      const r = await CalendarFeeds.create(scope);
      setHasToken(true);
      setScope(r.calendarFeed.scope);
      setLink(r.calendarFeed.url ?? null);
    } catch (err) {
      setError(errorText(err, "We couldn't make your calendar link."));
    } finally {
      setBusy(false);
    }
  }

  async function remake() {
    if (!confirm("Make a new calendar link? Your old link will stop working.")) return;
    await create();
  }

  async function turnOff() {
    if (!confirm("Turn off your calendar link? Calendar apps using it will stop updating.")) return;
    setBusy(true);
    setError(null);
    setStatus("");
    try {
      await CalendarFeeds.revoke();
      setHasToken(false);
      setLink(null);
      setStatus("Calendar link turned off.");
    } catch (err) {
      setError(errorText(err, "We couldn't turn off your calendar link."));
    } finally {
      setBusy(false);
    }
  }

  async function chooseScope(next: CalendarFeed["scope"]) {
    const prev = scope;
    setScope(next);
    if (!hasToken) return;
    setBusy(true);
    setError(null);
    setStatus("");
    try {
      await CalendarFeeds.setScope(next);
      setStatus("Feed choice saved.");
    } catch (err) {
      setScope(prev);
      setError(errorText(err, "We couldn't save that choice."));
    } finally {
      setBusy(false);
    }
  }

  async function copyLink() {
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
  }

  return (
    <div className="bg-surface rounded-lg border border-border p-6 space-y-4 mt-6">
      {showHeading && <h2 className="text-xl font-bold">Calendar feed</h2>}
      <p className="text-sm text-text-muted">
        Put your Fold events in the calendar app you already use. In your calendar
        app, choose Add calendar &rarr; From URL (or Subscribe) and paste this link.
        It keeps itself up to date.
      </p>
      {loadError ? (
        <ErrorNotice message={loadError} onRetry={load} />
      ) : !loaded ? (
        <Skeleton kind="lines" count={2} label="Loading your calendar link…" />
      ) : (
        <>
          {error && <FormError>{error}</FormError>}
          <Choices legend="Events in the feed">
            <Radio
              name="feed-scope"
              label="My events"
              description="Your groups' events, events you host, and the dates you're going or waitlisted for."
              checked={scope === "mine"}
              disabled={busy}
              onChange={() => void chooseScope("mine")}
            />
            <Radio
              name="feed-scope"
              label="Everything I can see"
              description="Every event you could find here in Fold."
              checked={scope === "all"}
              disabled={busy}
              onChange={() => void chooseScope("all")}
            />
          </Choices>
          {link ? (
            <Field
              id="calendar-feed-link"
              label="Your calendar link"
              hint="This link is shown once. Anyone with it can see these events, so keep it to yourself. Making a new link stops the old one."
            >
              <Input ref={linkRef} value={link} readOnly layout="font-mono text-sm" onFocus={(e) => e.currentTarget.select()} />
            </Field>
          ) : hasToken ? (
            <p className="text-sm text-text-muted">
              Your calendar link is on. We can&rsquo;t show it again, so make a new
              one if you need to set up another device.
            </p>
          ) : (
            <p className="text-sm text-text-muted">You don&rsquo;t have a calendar link yet.</p>
          )}
          {hasToken ? (
            <div className="flex flex-wrap gap-2">
              {link && (
                <Button variant="primary" layout="w-full sm:w-auto" onClick={() => void copyLink()}>
                  Copy link
                </Button>
              )}
              <Button variant="secondary" layout="w-full sm:w-auto" onClick={() => void remake()} disabled={busy}>
                {busy ? "Saving…" : "Make a new link"}
              </Button>
              <Button variant="danger-outline" layout="w-full sm:w-auto" onClick={() => void turnOff()} disabled={busy}>
                Turn off calendar link
              </Button>
            </div>
          ) : (
            <Button variant="primary" layout="w-full sm:w-auto" onClick={() => void create()} disabled={busy}>
              {busy ? "Saving…" : "Create calendar link"}
            </Button>
          )}
        </>
      )}
      <p role="status" className="text-success text-sm empty:hidden">{status}</p>
    </div>
  );
}
