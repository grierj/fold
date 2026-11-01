import { useCallback, useEffect, useId, useRef, useState, type ReactNode } from "react";
import { AdminIcon, BellIcon, CalendarIcon, CheckIcon, ChevronRightIcon, GroupsIcon, UserPlusIcon } from "./icons";
import { Link, NavLink, useNavigate } from "react-router-dom";
import { Button } from "./ui/Button";
import { EmptyState } from "./ui/EmptyState";
import { Illustration } from "./ui/Illustration";
import {
  badgeLabel, badgeText, kindIcon, relativeTime, resolutionText, useFeed, type Feed,
} from "../lib/notifications";
import type { Badge } from "../api";
import type { Notification } from "../types";

// The bell, its panel, and the rows shared with the /notifications page.
// Built to research 6.1–6.7, 6.9 and 6.10. Rows are plain text; links are
// internal paths from the server.

const MOBILE = "(max-width: 639px)";

function isMobile(): boolean {
  return typeof window.matchMedia === "function" && window.matchMedia(MOBILE).matches;
}

const KIND_ICONS = {
  calendar: CalendarIcon, person: UserPlusIcon, check: CheckIcon, group: GroupsIcon, shield: AdminIcon, generic: BellIcon,
} as const;

// The icon follows the kind, not the category (audit item 22). Decorative:
// the title carries the meaning. data-icon names the choice for tests.
function KindBadge({ kind }: { kind: string }) {
  const name = kindIcon(kind);
  const Glyph = KIND_ICONS[name];
  return (
    <span className="w-9 h-9 text-text-muted flex items-center justify-center shrink-0">
      <Glyph className="w-5 h-5" data-icon={name} />
    </span>
  );
}

// NotificationRow: the whole row links to the item and opens it (marks it
// read). Buttons and × sit outside the link (6.3).
function NotificationRow({
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
  // A decided task keeps only its outcome: the original ask is stale.
  const resolved = !!(done || handled);
  return (
    <li className="relative flex gap-3 px-4 py-4 hover:bg-sunken">
      {unread && <span className="absolute left-1.5 top-5 w-2 h-2 rounded-full bg-primary" aria-hidden="true" />}
      <KindBadge kind={n.kind} />
      <div className={`min-w-0 flex-1 ${n.archived ? "pr-20" : "pr-8"}`}>
        <Link
          to={n.link}
          onClick={() => {
            feed.open(n);
            onNavigate?.();
          }}
          className="block min-h-11 rounded-sm"
        >
          {unread && <span className="sr-only">Unread. </span>}
          {n.waiting && <span className="sr-only">Waiting on you. </span>}
          <span className={`block text-sm line-clamp-2 ${unread ? "font-semibold" : ""}`}>{n.title}</span>
          {n.body && !resolved && <span className="block text-sm text-text-muted line-clamp-2">{n.body}</span>}
        </Link>
        <div className="mt-1 text-sm text-text-muted flex flex-wrap gap-x-3 gap-y-1">
          <time dateTime={n.updatedAt} title={new Date(n.updatedAt).toLocaleString()}>
            {relativeTime(n.updatedAt)}
          </time>
          {n.waiting && <span className="font-semibold text-text-muted">Waiting on you</span>}
        </div>
        {handled ? (
          <p className="mt-1 text-sm text-text-muted">{handled}</p>
        ) : done ? (
          <p className="mt-1 text-sm text-text-muted">
            <span className="sr-only">Done. </span>
            {done}
          </p>
        ) : null}
        {n.waiting && n.actions.length > 0 && (
          <div className="mt-2 flex flex-wrap gap-3">
            {n.actions.map((a) => (
              <Button variant="primary"
                key={a.key}
                aria-busy={feed.busy[n.id] || undefined}
                disabled={feed.busy[n.id] || feed.offline}
                title={feed.offline ? "Reconnect to do this" : undefined}
                onClick={() => feed.approve(n)}
              >
                {feed.busy[n.id] ? "Approving…" : a.label}
              </Button>
            ))}
            <Link to={n.link} onClick={onNavigate} className="btn-secondary text-sm">
              Review
            </Link>
          </div>
        )}
        {feed.rowError[n.id] && <p className="mt-1 text-sm text-danger">{feed.rowError[n.id]}</p>}
      </div>
      {n.archived && (
        <button
          className="absolute right-1 top-1 h-11 px-3 text-sm text-primary font-semibold hover:underline"
          aria-label={`Restore: ${n.title}`}
          disabled={feed.offline}
          onClick={() => feed.unarchive(n).catch(() => {})}
        >
          Restore
        </button>
      )}
      {!n.waiting && !n.archived && (
        <button
          className="absolute right-1 top-1 w-11 h-11 flex items-center justify-center text-text-muted hover:text-text rounded-full"
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
function ArchivedToast({ item, onUndo, onClose }: { item: Notification; onUndo: () => void; onClose: () => void }) {
  const [paused, setPaused] = useState(false);
  useEffect(() => {
    if (paused) return;
    const t = setTimeout(onClose, 8000);
    return () => clearTimeout(t);
  }, [paused, onClose, item.id]);
  return (
    <div
      role="status"
      className="fixed bottom-4 left-1/2 -translate-x-1/2 z-30 bg-inverse text-inverse-foreground text-sm rounded-md px-4 py-2 flex gap-3 items-center shadow-lg"
      onMouseEnter={() => setPaused(true)}
      onMouseLeave={() => setPaused(false)}
      onFocus={() => setPaused(true)}
      onBlur={() => setPaused(false)}
    >
      Archived
      <button className="min-h-11 min-w-11 font-semibold underline" onClick={onUndo}>
        Undo
      </button>
    </div>
  );
}

function StatusLine({ feed }: { feed: Feed }) {
  const at = feed.loadedAt?.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  if (feed.offline) {
    return <p className="px-4 py-1 text-xs text-text-muted">You're offline. Showing what was loaded{at ? ` at ${at}` : ""}.</p>;
  }
  if (feed.error && feed.loadedAt) {
    return (
      <p className="px-4 py-1 text-xs text-text-muted">
        Couldn't refresh. Showing alerts from {at}.{" "}
        <button className="min-h-11 underline" onClick={feed.reload}>
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
        <li key={i} className="h-10 bg-sunken rounded-sm motion-safe:animate-pulse" />
      ))}
    </ul>
  );
}

function FeedEmpty({ text, action }: { text?: string; action?: ReactNode }) {
  return (
    <div className="px-4"><EmptyState
      framed={false}
      illustration={<Illustration name="all-caught-up" size="sm" />}
      title={text ?? "You're all caught up"}
      body={!text ? "Updates about your groups and events show up here." : undefined}
      action={!text ? action : undefined}
    /></div>
  );
}

// FeedBody renders loading, error, and the rows of one feed.
export function FeedBody({
  feed, onArchived, onNavigate, emptyText, emptyAction, sections = true, dayHeadings = false,
}: {
  feed: Feed;
  onArchived: (n: Notification) => void;
  onNavigate?: () => void;
  emptyText?: string;
  emptyAction?: ReactNode; // shown with the default empty message only
  sections?: boolean;
  dayHeadings?: boolean; // the full page splits Recent into Today and Earlier (6.5)
}) {
  const headingId = useId();
  if (feed.loading && !feed.loadedAt) return <Skeleton />;
  if (feed.error && !feed.loadedAt) {
    return (
      <div className="px-4 py-6 text-center text-sm">
        <p className="text-text-muted">Couldn't load alerts.</p>
        <Button variant="secondary" layout="mt-2" onClick={feed.reload}>
          Try again
        </Button>
      </div>
    );
  }
  if (feed.waiting.length + feed.items.length === 0) return <FeedEmpty text={emptyText} action={emptyAction} />;
  const row = (n: Notification) => (
    <NotificationRow key={n.id} n={n} feed={feed} onArchived={onArchived} onNavigate={onNavigate} />
  );
  return (
    <>
      <StatusLine feed={feed} />
      {feed.waiting.length > 0 && (
        <section aria-labelledby={`${headingId}-w`}>
          <h3 id={`${headingId}-w`} className="px-4 pt-4 pb-2 text-sm font-semibold text-text-muted">
            Waiting on you
          </h3>
          <ul className="divide-y divide-border">{feed.waiting.map(row)}</ul>
          {feed.waitingTotal > feed.waiting.length && (
            <Link to="/notifications?filter=waiting" onClick={onNavigate} className="flex min-h-11 items-center gap-2 px-4 py-2 text-sm text-primary font-semibold hover:underline">
              and {feed.waitingTotal - feed.waiting.length} more <ChevronRightIcon className="h-4 w-4 shrink-0" />
            </Link>
          )}
        </section>
      )}
      {feed.items.length > 0 &&
        (dayHeadings ? (
          <>
            {splitByDay(feed.items).map(([label, rows]) => (
              <section key={label} aria-labelledby={`${headingId}-${label}`}>
                <h3 id={`${headingId}-${label}`} className="px-4 pt-4 pb-2 text-sm font-semibold text-text-muted">
                  {label}
                </h3>
                <ul className="divide-y divide-border">{rows.map(row)}</ul>
              </section>
            ))}
          </>
        ) : (
          <section aria-labelledby={`${headingId}-r`}>
            {sections && feed.waiting.length > 0 && (
              <h3 id={`${headingId}-r`} className="px-4 pt-4 pb-2 text-sm font-semibold text-text-muted">
                Recent
              </h3>
            )}
            <ul className="divide-y divide-border">{feed.items.map(row)}</ul>
          </section>
        ))}
      {feed.next && (
        <button className="block min-h-11 w-full px-4 py-2 text-sm text-primary font-semibold hover:underline" disabled={feed.loadingMore} onClick={feed.showMore}>
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
export function NotificationBell({ badge }: { badge: Badge & { loaded: boolean } }) {
  const navigate = useNavigate();
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
      setAnnounce(`${more} new alert${more === 1 ? "" : "s"}`);
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
        className="relative flex items-center justify-center min-h-11 min-w-11 text-text-muted hover:text-primary rounded-full"
        aria-label={`${count > 0 ? `${badgeText(badge)} ` : ""}${badgeLabel(badge)}`}
        aria-expanded={open}
        aria-controls="notif-panel"
        title="Alerts"
      >
        <BellIcon />
        {count > 0 && (
          <span
            aria-hidden="true"
            className="absolute top-0.5 right-0.5 min-w-5 h-5 px-1 rounded-full bg-primary-hover text-primary-foreground text-xs font-bold flex items-center justify-center ring-2 ring-surface"
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
          className="absolute right-0 mt-2 w-96 max-w-[calc(100vw-2rem)] bg-surface rounded-lg border border-border shadow-lg z-20 overflow-hidden flex flex-col max-h-[70vh]"
        >
          <div className="px-4 py-3 border-b border-border flex items-center gap-3">
            <h2 id="notif-panel-heading" ref={headingRef} tabIndex={-1} className="font-bold text-sm mr-auto outline-hidden">
              Alerts
            </h2>
            <button
              className="inline-flex items-center min-h-11 text-sm text-primary font-semibold hover:underline aria-disabled:text-text-muted aria-disabled:no-underline"
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
            className="flex min-h-11 items-center justify-center px-4 py-2.5 border-t border-border text-sm text-center text-primary font-semibold hover:underline"
          >
            See all alerts
          </Link>
        </section>
      )}
      {toast}
    </div>
  );
}

// NotificationTab: the phone tab bar's Notifications tab (UI makeover PR 5),
// with the same badge as the bell.
export function NotificationTab({ badge, className }: { badge: Badge; className: (a: { isActive: boolean }) => string }) {
  const count = badge.unread + badge.waiting;
  return (
    <NavLink to="/notifications" className={className} aria-label={`${count > 0 ? `${badgeText(badge)} ` : ""}${badgeLabel(badge)}`}>
      <span className="relative">
        <BellIcon />
        {count > 0 && (
          <span aria-hidden="true" className="absolute -top-1.5 -right-2.5 min-w-5 h-5 px-1 rounded-full bg-primary-hover text-primary-foreground text-xs font-bold flex items-center justify-center ring-2 ring-surface">
            {badgeText(badge)}
          </span>
        )}
      </span>{" "}
      {/* "Alerts" so five tabs fit at 390 without wrapping (DEC-67). */}
      <span aria-hidden="true">Alerts</span>
    </NavLink>
  );
}
