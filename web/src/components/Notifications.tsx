import { useCallback, useEffect, useId, useRef, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import {
  badgeLabel, badgeText, relativeTime, resolutionText, useBadge, useFeed, type Feed,
} from "../lib/notifications";
import type { Notification } from "../types";

// The bell, its panel, and the rows shared with the /notifications page.
// Built to research 6.1–6.7, 6.9 and 6.10. Rows are plain text; links are
// internal paths from the server.

const MOBILE = "(max-width: 639px)";

function isMobile(): boolean {
  return typeof window.matchMedia === "function" && window.matchMedia(MOBILE).matches;
}

function BellIcon() {
  return (
    <svg className="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth={2}
        d="M15 17h5l-1.405-1.405A2.032 2.032 0 0118 14.158V11a6.002 6.002 0 00-4-5.659V5a2 2 0 10-4 0v.341C7.67 6.165 6 8.388 6 11v3.159c0 .538-.214 1.055-.595 1.436L4 17h5m6 0v1a3 3 0 11-6 0v-1m6 0H9"
      />
    </svg>
  );
}

function CategoryIcon({ category }: { category: Notification["category"] }) {
  const d =
    category === "events"
      ? "M8 7V3m8 4V3m-9 8h10M5 21h14a2 2 0 002-2V7a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z"
      : category === "admin"
        ? "M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z"
        : "M17 20h5v-2a3 3 0 00-5.356-1.857M17 20H7m10 0v-2c0-.656-.126-1.283-.356-1.857M7 20H2v-2a3 3 0 015.356-1.857M7 20v-2c0-.656.126-1.283.356-1.857m0 0a5.002 5.002 0 019.288 0M15 7a3 3 0 11-6 0 3 3 0 016 0z";
  return (
    <span className="w-9 h-9 rounded-full bg-gray-100 text-gray-500 flex items-center justify-center shrink-0">
      <svg className="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
        <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d={d} />
      </svg>
    </span>
  );
}

// NotificationRow: the whole row links to the item and opens it (marks it
// read). Buttons and × sit outside the link (6.3).
export function NotificationRow({
  n, feed, onArchived, onNavigate,
}: {
  n: Notification;
  feed: Feed;
  onArchived: (n: Notification) => void;
  onNavigate?: () => void;
}) {
  const unread = !n.read && !n.waiting && !n.archived;
  const done = resolutionText(n);
  const handled = feed.handled[n.id];
  return (
    <li className="relative flex gap-3 px-4 py-3 hover:bg-gray-50">
      {unread && <span className="absolute left-1.5 top-5 w-2 h-2 rounded-full bg-brand" aria-hidden="true" />}
      <CategoryIcon category={n.category} />
      <div className="min-w-0 flex-1 pr-8">
        <Link
          to={n.link}
          onClick={() => {
            feed.open(n);
            onNavigate?.();
          }}
          className="block focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand rounded"
        >
          {unread && <span className="sr-only">Unread. </span>}
          {n.waiting && <span className="sr-only">Waiting on you. </span>}
          <span className={`block text-sm line-clamp-2 ${unread ? "font-semibold" : ""}`}>{n.title}</span>
          {n.body && <span className="block text-xs text-gray-500 line-clamp-2">{n.body}</span>}
        </Link>
        <div className="mt-0.5 text-xs text-gray-500 flex gap-2">
          <time dateTime={n.updatedAt} title={new Date(n.updatedAt).toLocaleString()}>
            {relativeTime(n.updatedAt)}
          </time>
          {n.waiting && <span className="font-semibold text-gray-700">Waiting on you</span>}
        </div>
        {handled ? (
          <p className="mt-1 text-sm text-gray-600">{handled}</p>
        ) : done ? (
          <p className="mt-1 text-sm text-gray-600">
            <span className="sr-only">Done. </span>
            {done}
          </p>
        ) : null}
        {n.waiting && n.actions.length > 0 && (
          <div className="mt-2 flex flex-wrap gap-3">
            {n.actions.map((a) => (
              <button
                key={a.key}
                className="btn-primary !py-1.5 !px-3 text-sm"
                aria-busy={feed.busy[n.id] || undefined}
                disabled={feed.busy[n.id] || feed.offline}
                title={feed.offline ? "Reconnect to do this" : undefined}
                onClick={() => feed.approve(n)}
              >
                {feed.busy[n.id] ? "Approving…" : a.label}
              </button>
            ))}
            <Link to={n.link} onClick={onNavigate} className="btn-secondary !py-1.5 !px-3 text-sm">
              Review
            </Link>
          </div>
        )}
        {feed.rowError[n.id] && <p className="mt-1 text-sm text-red-600">{feed.rowError[n.id]}</p>}
      </div>
      {n.archived && (
        <button
          className="absolute right-1 top-1 h-11 px-3 text-sm text-brand font-semibold hover:underline"
          aria-label={`Restore: ${n.title}`}
          disabled={feed.offline}
          onClick={() => feed.unarchive(n).catch(() => {})}
        >
          Restore
        </button>
      )}
      {!n.waiting && !n.archived && (
        <button
          className="absolute right-1 top-1 w-11 h-11 flex items-center justify-center text-gray-500 hover:text-gray-800 rounded-full"
          aria-label={`Archive: ${n.title}`}
          disabled={feed.offline}
          title={feed.offline ? "Reconnect to do this" : "Archive"}
          onClick={() => feed.archive(n).then(() => onArchived(n)).catch(() => {})}
        >
          <span aria-hidden="true">×</span>
        </button>
      )}
    </li>
  );
}

// ArchivedToast: "Archived · Undo", 8 s, paused while hovered or focused
// (WCAG 2.2.1).
export function ArchivedToast({ item, onUndo, onClose }: { item: Notification; onUndo: () => void; onClose: () => void }) {
  const [paused, setPaused] = useState(false);
  useEffect(() => {
    if (paused) return;
    const t = setTimeout(onClose, 8000);
    return () => clearTimeout(t);
  }, [paused, onClose, item.id]);
  return (
    <div
      role="status"
      className="fixed bottom-4 left-1/2 -translate-x-1/2 z-30 bg-gray-900 text-white text-sm rounded-lg px-4 py-2 flex gap-3 items-center shadow-lg"
      onMouseEnter={() => setPaused(true)}
      onMouseLeave={() => setPaused(false)}
      onFocus={() => setPaused(true)}
      onBlur={() => setPaused(false)}
    >
      Archived
      <button className="font-semibold underline" onClick={onUndo}>
        Undo
      </button>
    </div>
  );
}

function StatusLine({ feed }: { feed: Feed }) {
  const at = feed.loadedAt?.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  if (feed.offline) {
    return <p className="px-4 py-1 text-xs text-gray-500">You're offline. Showing what was loaded{at ? ` at ${at}` : ""}.</p>;
  }
  if (feed.error && feed.loadedAt) {
    return (
      <p className="px-4 py-1 text-xs text-gray-500">
        Couldn't refresh. Showing notifications from {at} ·{" "}
        <button className="underline" onClick={feed.reload}>
          Try again
        </button>
      </p>
    );
  }
  return null;
}

function Skeleton() {
  return (
    <ul aria-busy="true" className="px-4 py-3 space-y-3">
      {[0, 1, 2].map((i) => (
        <li key={i} className="h-10 bg-gray-100 rounded motion-safe:animate-pulse" />
      ))}
    </ul>
  );
}

export function EmptyState({ text }: { text?: string }) {
  return (
    <div className="px-4 py-8 text-center">
      <svg className="w-10 h-10 mx-auto text-gray-300" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
        <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={1.5} d="M4 4h10l6 6v10H4zM14 4v6h6" />
      </svg>
      <p className="mt-2 text-sm font-semibold text-gray-700">{text ?? "You're all caught up"}</p>
      {!text && <p className="text-xs text-gray-500">Updates about your groups, events, and requests show up here.</p>}
    </div>
  );
}

// FeedBody renders loading, error, and the rows of one feed.
export function FeedBody({
  feed, onArchived, onNavigate, emptyText, sections = true, dayHeadings = false,
}: {
  feed: Feed;
  onArchived: (n: Notification) => void;
  onNavigate?: () => void;
  emptyText?: string;
  sections?: boolean;
  dayHeadings?: boolean; // the full page splits Recent into Today and Earlier (6.5)
}) {
  const headingId = useId();
  if (feed.loading && !feed.loadedAt) return <Skeleton />;
  if (feed.error && !feed.loadedAt) {
    return (
      <div className="px-4 py-6 text-center text-sm">
        <p className="text-gray-700">Couldn't load notifications.</p>
        <button className="btn-secondary mt-2 !py-1.5 !px-3 text-sm" onClick={feed.reload}>
          Try again
        </button>
      </div>
    );
  }
  if (feed.waiting.length + feed.items.length === 0) return <EmptyState text={emptyText} />;
  const row = (n: Notification) => (
    <NotificationRow key={n.id} n={n} feed={feed} onArchived={onArchived} onNavigate={onNavigate} />
  );
  return (
    <>
      <StatusLine feed={feed} />
      {feed.waiting.length > 0 && (
        <section aria-labelledby={`${headingId}-w`}>
          <h3 id={`${headingId}-w`} className="px-4 pt-3 text-xs font-semibold text-gray-700 uppercase tracking-wide">
            Waiting on you
          </h3>
          <ul className="divide-y divide-gray-100">{feed.waiting.map(row)}</ul>
          {feed.waitingTotal > feed.waiting.length && (
            <Link to="/notifications?filter=waiting" onClick={onNavigate} className="block px-4 py-2 text-sm text-brand font-semibold hover:underline">
              and {feed.waitingTotal - feed.waiting.length} more →
            </Link>
          )}
        </section>
      )}
      {feed.items.length > 0 &&
        (dayHeadings ? (
          <>
            {splitByDay(feed.items).map(([label, rows]) => (
              <section key={label} aria-labelledby={`${headingId}-${label}`}>
                <h3 id={`${headingId}-${label}`} className="px-4 pt-3 text-xs font-semibold text-gray-700 uppercase tracking-wide">
                  {label}
                </h3>
                <ul className="divide-y divide-gray-100">{rows.map(row)}</ul>
              </section>
            ))}
          </>
        ) : (
          <section aria-labelledby={`${headingId}-r`}>
            {sections && feed.waiting.length > 0 && (
              <h3 id={`${headingId}-r`} className="px-4 pt-3 text-xs font-semibold text-gray-700 uppercase tracking-wide">
                Recent
              </h3>
            )}
            <ul className="divide-y divide-gray-100">{feed.items.map(row)}</ul>
          </section>
        ))}
      {feed.next && (
        <button className="block w-full px-4 py-2 text-sm text-brand font-semibold hover:underline" disabled={feed.loadingMore} onClick={feed.showMore}>
          {feed.loadingMore ? "Loading…" : "Show more"}
        </button>
      )}
    </>
  );
}

function splitByDay(rows: Notification[]): [string, Notification[]][] {
  const today = new Date().toDateString();
  const t = rows.filter((n) => new Date(n.updatedAt).toDateString() === today);
  const e = rows.filter((n) => new Date(n.updatedAt).toDateString() !== today);
  const out: [string, Notification[]][] = [];
  if (t.length) out.push(["Today", t]);
  if (e.length) out.push(["Earlier", e]);
  return out;
}

// useArchiveToast keeps the last archived row for the Undo toast.
export function useArchiveToast(feed: Feed) {
  const [toast, setToast] = useState<Notification | null>(null);
  const close = useCallback(() => setToast(null), []);
  const node = toast ? (
    <ArchivedToast
      item={toast}
      onClose={close}
      onUndo={() => {
        feed.unarchive(toast).catch(() => {});
        setToast(null);
      }}
    />
  ) : null;
  return { onArchived: setToast, toast: node };
}

// NotificationBell: the badge counts unread plus waiting items (DEC-44),
// capped at 9+. On narrow screens it goes to /notifications (6.10); wider,
// it opens a disclosure popover (no focus trap, 6.2).
export function NotificationBell() {
  const { pathname, search } = useLocation();
  const navigate = useNavigate();
  const badge = useBadge(pathname + search);
  const [open, setOpen] = useState(false);
  const boxRef = useRef<HTMLDivElement>(null);
  const bellRef = useRef<HTMLButtonElement>(null);
  const headingRef = useRef<HTMLHeadingElement>(null);
  const feed = useFeed("all", open);
  const { onArchived, toast } = useArchiveToast(feed);
  const count = badge.unread + badge.waiting;

  // The count increases are announced at most once a minute (6.9).
  const [announce, setAnnounce] = useState("");
  const last = useRef({ count: -1, at: 0 });
  useEffect(() => {
    if (!badge.loaded) return;
    const now = Date.now();
    if (last.current.count >= 0 && count > last.current.count && now - last.current.at > 60_000) {
      const more = count - last.current.count;
      setAnnounce(`${more} new notification${more === 1 ? "" : "s"}`);
      last.current.at = now;
    }
    last.current.count = count;
  }, [count, badge.loaded]);

  const close = useCallback((focusBell: boolean) => {
    setOpen(false);
    if (focusBell) bellRef.current?.focus();
  }, []);

  useEffect(() => {
    if (!open) return;
    headingRef.current?.focus();
    function onDocClick(e: MouseEvent) {
      const t = e.target as Node;
      // A row removed by its own click (×) is no longer in the document.
      if (boxRef.current && document.contains(t) && !boxRef.current.contains(t)) setOpen(false);
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") close(true);
    }
    document.addEventListener("click", onDocClick);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("click", onDocClick);
      document.removeEventListener("keydown", onKey);
    };
  }, [open, close]);

  const ordinaryUnread = badge.unread > 0;
  return (
    <div
      ref={boxRef}
      className="relative"
      onBlur={(e) => {
        // Tabbing past the last control closes the panel (6.2).
        if (open && !e.currentTarget.contains(e.relatedTarget as Node | null) && e.relatedTarget) setOpen(false);
      }}
    >
      <button
        ref={bellRef}
        onClick={() => (isMobile() ? navigate("/notifications") : setOpen((o) => !o))}
        className="relative text-gray-700 hover:text-brand p-2.5 -m-2.5 rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand focus-visible:ring-offset-2"
        aria-label={badgeLabel(badge)}
        aria-expanded={open}
        aria-controls="notif-panel"
        title="Notifications"
      >
        <BellIcon />
        {count > 0 && (
          <span
            aria-hidden="true"
            className="absolute top-0.5 right-0.5 min-w-[1.25rem] h-5 px-1 rounded-full bg-brand-dark text-white text-xs font-bold flex items-center justify-center ring-2 ring-white"
          >
            {badgeText(badge)}
          </span>
        )}
      </button>
      <span className="sr-only" aria-live="polite">
        {announce}
      </span>
      {open && (
        <section
          id="notif-panel"
          aria-labelledby="notif-panel-heading"
          className="absolute right-0 mt-2 w-96 max-w-[calc(100vw-2rem)] bg-white rounded-xl border border-gray-200 shadow-lg z-20 overflow-hidden flex flex-col max-h-[70vh]"
        >
          <div className="px-4 py-3 border-b border-gray-100 flex items-center gap-3">
            <h2 id="notif-panel-heading" ref={headingRef} tabIndex={-1} className="font-bold text-sm mr-auto outline-none">
              Notifications
            </h2>
            <button
              className="text-sm text-brand font-semibold hover:underline aria-disabled:text-gray-500 aria-disabled:no-underline"
              aria-disabled={!ordinaryUnread}
              title="Items waiting on you stay until they're handled"
              onClick={() => ordinaryUnread && feed.readAll()}
            >
              Mark all as read
            </button>
          </div>
          <div className="overflow-y-auto">
            <FeedBody feed={feed} onArchived={onArchived} onNavigate={() => setOpen(false)} />
          </div>
          <Link
            to="/notifications"
            onClick={() => setOpen(false)}
            className="block px-4 py-2.5 border-t border-gray-100 text-sm text-center text-brand font-semibold hover:underline"
          >
            See all notifications
          </Link>
        </section>
      )}
      {toast}
    </div>
  );
}
