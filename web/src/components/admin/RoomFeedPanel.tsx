import { useCallback, useEffect, useRef, useState } from "react";
import { AdminRooms } from "../../api";
import type { Room } from "../../types";
import { errorText } from "../../lib/errors";
import { formatEventDay } from "../../lib/when";
import { Button } from "../ui/Button";
import { Field, FormError, Input } from "../ui/Field";
import { Skeleton } from "../ui/Skeleton";
import { ErrorNotice } from "../ErrorNotice";

// A room's calendar feed (M2.2b, 8.2): an admin makes a secret link the
// office or a calendar app can subscribe to. It behaves like the personal
// CalendarFeedCard: the link shows once, a new link stops the old one, and
// a failed load never looks like "no link yet".
export function RoomFeedPanel({ room, onClose }: { room: Room; onClose: () => void }) {
  const [loaded, setLoaded] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [hasToken, setHasToken] = useState(false);
  const [createdAt, setCreatedAt] = useState<string | null>(null);
  const [link, setLink] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState("");
  const linkRef = useRef<HTMLInputElement>(null);
  const headingRef = useRef<HTMLHeadingElement>(null);
  const alive = useRef(true);

  const load = useCallback(() => {
    setLoaded(false);
    setLoadError(null);
    AdminRooms.getFeed(room.id)
      .then((r) => {
        if (!alive.current) return;
        setHasToken(r.feed.hasToken);
        setCreatedAt(r.feed.createdAt);
        setLoaded(true);
      })
      .catch((err) => {
        if (alive.current) setLoadError(errorText(err, "We couldn't load this room's calendar link."));
      });
  }, [room.id]);

  useEffect(() => {
    alive.current = true;
    load();
    // The button that opened the panel is gone, so focus starts on its heading.
    headingRef.current?.focus();
    return () => {
      alive.current = false;
    };
  }, [load]);

  async function create() {
    setBusy(true);
    setError(null);
    setStatus("");
    try {
      const r = await AdminRooms.makeFeed(room.id);
      setHasToken(true);
      setCreatedAt(r.feed.createdAt);
      setLink(r.feed.url ?? null);
    } catch (err) {
      setError(errorText(err, "We couldn't make the calendar link."));
    } finally {
      setBusy(false);
    }
  }

  async function remake() {
    if (!confirm(`Make a new calendar link for ${room.name}? The old link will stop working.`)) return;
    await create();
  }

  async function turnOff() {
    if (!confirm(`Turn off the calendar link for ${room.name}? Calendar apps using it will stop updating.`)) return;
    setBusy(true);
    setError(null);
    setStatus("");
    try {
      await AdminRooms.revokeFeed(room.id);
      setHasToken(false);
      setCreatedAt(null);
      setLink(null);
      setStatus("Calendar link turned off.");
    } catch (err) {
      setError(errorText(err, "We couldn't turn off the calendar link."));
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
    <section aria-labelledby={`room-feed-${room.id}`} className="bg-surface rounded-lg border border-border p-6 space-y-4 max-w-xl">
      <h3 id={`room-feed-${room.id}`} ref={headingRef} tabIndex={-1} className="text-lg font-bold focus:outline-none">
        Calendar feed for {room.name}
      </h3>
      <p className="text-sm text-text-muted">
        Follow this room&rsquo;s bookings in a calendar app, like the church office calendar.
        In the app, choose Add calendar &rarr; From URL (or Subscribe) and paste the link.
        Dates most people can&rsquo;t see show as Reserved.
      </p>
      {loadError ? (
        <ErrorNotice message={loadError} onRetry={load} />
      ) : !loaded ? (
        <Skeleton kind="lines" count={2} label="Loading the room's calendar link…" />
      ) : (
        <>
          {error && <FormError>{error}</FormError>}
          {link ? (
            <Field
              id={`room-feed-link-${room.id}`}
              label="Calendar link"
              hint="Save this link now; we won't show it again. Anyone with it can see this room's bookings. Making a new link stops the old one."
            >
              <Input ref={linkRef} value={link} readOnly layout="font-mono text-sm" onFocus={(e) => e.currentTarget.select()} />
            </Field>
          ) : hasToken ? (
            <p className="text-sm text-text-muted">
              A calendar link is on{createdAt ? `, made ${formatEventDay(createdAt)}` : ""}. We can&rsquo;t show it
              again, so make a new one if you need to set it up somewhere else.
            </p>
          ) : (
            <p className="text-sm text-text-muted">This room doesn&rsquo;t have a calendar link yet.</p>
          )}
          <div className="flex flex-wrap gap-2">
            {hasToken ? (
              <>
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
              </>
            ) : (
              <Button variant="primary" layout="w-full sm:w-auto" onClick={() => void create()} disabled={busy}>
                {busy ? "Saving…" : "Create calendar link"}
              </Button>
            )}
          </div>
        </>
      )}
      <Button variant="ghost" layout="w-full sm:w-auto" onClick={onClose}>
        Done
      </Button>
      <p role="status" className="text-success text-sm empty:hidden">{status}</p>
    </section>
  );
}
