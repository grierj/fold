import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, Notifications as Api, type Badge } from "../api";
import type { Notification, NotificationFilter } from "../types";

// Everything here lives in memory only: no notification state goes to
// localStorage or sessionStorage (5.12, research 6.13 #7), so logout's full
// reload leaves nothing behind.

const CHANGED = "fold:notifications-changed";

// notificationsChanged asks the bell and any open list to refetch (after
// an archive, a read, or an inline action).
function notificationsChanged() {
  window.dispatchEvent(new Event(CHANGED));
}

const POLL_MS = 60_000;

// useBadge polls GET /notifications/badge every 60 s while the tab is
// visible, and refetches on focus, on navigation (refreshKey), when coming
// back online, and after any change. It pauses while the tab is hidden.
export function useBadge(refreshKey: string, enabled = true): Badge & { loaded: boolean } {
  const [badge, setBadge] = useState<Badge & { loaded: boolean }>({ unread: 0, waiting: 0, loaded: false });
  const etag = useRef("");
  const load = useCallback(async () => {
    if (document.visibilityState === "hidden") return;
    try {
      const got = await Api.badge(etag.current);
      if (got) {
        etag.current = got.etag;
        setBadge({ ...got.badge, loaded: true });
      }
    } catch {
      // Keep the last value; the bell never breaks the header.
    }
  }, []);

  useEffect(() => {
    if (enabled) load();
  }, [load, refreshKey, enabled]);

  useEffect(() => {
    if (!enabled) return;
    const onVisible = () => {
      if (document.visibilityState === "visible") load();
    };
    window.addEventListener("focus", load);
    window.addEventListener("online", load);
    window.addEventListener(CHANGED, load);
    document.addEventListener("visibilitychange", onVisible);
    const t = setInterval(load, POLL_MS);
    return () => {
      window.removeEventListener("focus", load);
      window.removeEventListener("online", load);
      window.removeEventListener(CHANGED, load);
      document.removeEventListener("visibilitychange", onVisible);
      clearInterval(t);
    };
  }, [load, enabled]);
  return badge;
}

export function badgeLabel(b: Badge): string {
  const parts = [];
  if (b.unread > 0) parts.push(`${b.unread} unread`);
  if (b.waiting > 0) parts.push(`${b.waiting} waiting on you`);
  return parts.length ? `Alerts, ${parts.join(", ")}` : "Alerts";
}

export function badgeText(b: Badge): string {
  const n = b.unread + b.waiting;
  return n > 9 ? "9+" : String(n);
}

export interface Feed {
  waiting: Notification[];
  waitingTotal: number;
  items: Notification[];
  next: string;
  loading: boolean;
  loadingMore: boolean;
  error: boolean; // the last refresh failed
  loadedAt: Date | null;
  offline: boolean;
  reload: () => void;
  showMore: () => void;
  open: (n: Notification) => void;
  archive: (n: Notification) => Promise<void>;
  unarchive: (n: Notification) => Promise<void>;
  readAll: () => Promise<void>;
  approve: (n: Notification) => Promise<void>;
  rowError: Record<number, string>;
  busy: Record<number, boolean>;
  handled: Record<number, string>; // "Already approved by Pat" after a lost race
}

// useFeed loads one filter of the feed and keeps it in memory. A failed
// refresh keeps what was loaded (stale-on-error, 6.4).
export function useFeed(filter: NotificationFilter, enabled = true): Feed {
  const [state, setState] = useState({
    waiting: [] as Notification[], waitingTotal: 0, items: [] as Notification[], next: "",
  });
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState(false);
  const [loadedAt, setLoadedAt] = useState<Date | null>(null);
  const [offline, setOffline] = useState(typeof navigator !== "undefined" && navigator.onLine === false);
  const [rowError, setRowError] = useState<Record<number, string>>({});
  const [busy, setBusy] = useState<Record<number, boolean>>({});
  const [handled, setHandled] = useState<Record<number, string>>({});

  // Each reload (and each filter change) starts a new revision; a response
  // from an older revision is dropped so it can't overwrite newer rows.
  const rev = useRef(0);

  const reload = useCallback(async () => {
    const mine = ++rev.current;
    try {
      const f = await Api.list(filter);
      if (mine !== rev.current) return;
      setState({ waiting: f.waiting, waitingTotal: f.waitingTotal, items: f.notifications, next: f.next });
      setError(false);
      setLoadedAt(new Date());
      if (f.notifications.length + f.waiting.length > 0) {
        Api.seen([...f.waiting, ...f.notifications].map((n) => n.id)).catch(() => {});
      }
    } catch {
      if (mine === rev.current) setError(true);
    } finally {
      if (mine === rev.current) setLoading(false);
    }
  }, [filter]);

  useEffect(() => {
    if (!enabled) return;
    setLoading(true);
    reload();
    window.addEventListener(CHANGED, reload);
    const on = () => setOffline(false);
    const off = () => setOffline(true);
    window.addEventListener("online", on);
    window.addEventListener("offline", off);
    return () => {
      window.removeEventListener(CHANGED, reload);
      window.removeEventListener("online", on);
      window.removeEventListener("offline", off);
      rev.current++;
    };
  }, [reload, enabled]);

  const showMore = useCallback(async () => {
    if (!state.next) return;
    const mine = rev.current;
    setLoadingMore(true);
    try {
      const f = await Api.list(filter, state.next);
      if (mine !== rev.current) return;
      setState((s) => ({ ...s, items: [...s.items, ...f.notifications], next: f.next }));
    } catch {
      if (mine === rev.current) setError(true);
    } finally {
      setLoadingMore(false);
    }
  }, [filter, state.next]);

  const patch = (id: number, p: Partial<Notification>) =>
    setState((s) => ({
      ...s,
      waiting: s.waiting.map((n) => (n.id === id ? { ...n, ...p } : n)),
      items: s.items.map((n) => (n.id === id ? { ...n, ...p } : n)),
    }));

  const open = useCallback((n: Notification) => {
    if (n.read) return;
    patch(n.id, { read: true });
    Api.read(n.id).then(notificationsChanged).catch(() => {});
  }, []);

  const archive = useCallback(async (n: Notification) => {
    await Api.archive(n.id, true);
    setState((s) => ({ ...s, items: s.items.filter((x) => x.id !== n.id) }));
    notificationsChanged();
  }, []);

  const unarchive = useCallback(async (n: Notification) => {
    await Api.archive(n.id, false);
    notificationsChanged();
  }, []);

  const readAll = useCallback(async () => {
    try {
      await Api.readAll();
    } catch {
      // Like the archive actions: the rows stay as they were, so nothing
      // changed on screen. (The "Couldn't refresh" line is about reloads.)
    }
    notificationsChanged();
  }, []);

  const approve = useCallback(async (n: Notification) => {
    if (!n.subjectId) return;
    setBusy((b) => ({ ...b, [n.id]: true }));
    setRowError((e) => ({ ...e, [n.id]: "" }));
    try {
      // Inline Approve exists on exactly two kinds (DEC-44); the server
      // allow-lists both and refuses anything else (6.6).
      if (n.kind === "account.requested") {
        await Api.approveAccountRequest(n.subjectId, n.id);
      } else {
        await Api.approveGroup(n.subjectId, n.id);
      }
      patch(n.id, {
        waiting: false, actions: [],
        resolution: { outcome: "approved", by: "", byMe: true, at: new Date().toISOString() },
      });
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        // Lost the race: not an error, someone else handled it (6.4).
        const outcome = String(err.data.resolution ?? "approved");
        const by = String(err.data.resolvedBy ?? "someone");
        patch(n.id, { waiting: false, actions: [], resolution: { outcome, by, byMe: false, at: new Date().toISOString() } });
        setHandled((h) => ({ ...h, [n.id]: `Already ${OUTCOME[outcome] ?? "handled"} by ${by}` }));
      } else {
        setRowError((e) => ({ ...e, [n.id]: "Couldn't approve. Try again or open the request." }));
      }
    } finally {
      setBusy((b) => ({ ...b, [n.id]: false }));
      notificationsChanged();
    }
  }, []);

  return {
    ...state, loading, loadingMore, error, loadedAt, offline, reload, showMore,
    open, archive, unarchive, readAll, approve, rowError, busy, handled,
  };
}

// relativeTime: "5m", "2h", "Yesterday", "Mar 3".
export function relativeTime(iso: string, now = new Date()): string {
  const t = new Date(iso);
  const mins = Math.round((now.getTime() - t.getTime()) / 60000);
  if (mins < 1) return "now";
  if (mins < 60) return `${mins}m`;
  const hours = Math.round(mins / 60);
  if (hours < 24) return `${hours}h`;
  const days = Math.round(hours / 24);
  if (days === 1) return "Yesterday";
  if (days < 7) return new Intl.RelativeTimeFormat(undefined, { numeric: "auto" }).format(-days, "day");
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" }).format(t);
}

const OUTCOME: Record<string, string> = {
  approved: "approved", confirmed: "confirmed", rejected: "rejected", deleted: "deleted", kept: "kept", withdrawn: "withdrawn",
};

// resolutionText: "You approved this" or "Done · approved by Pat".
export function resolutionText(n: Notification): string {
  const r = n.resolution;
  if (!r) return "";
  const verb = OUTCOME[r.outcome] ?? "handled";
  if (r.byMe) return `You ${verb} this`;
  if (r.outcome === "withdrawn") return "Done · withdrawn by " + (r.by || "the leader");
  return `Done · ${verb} by ${r.by || "someone"}`;
}

export type KindIcon = "calendar" | "person" | "check" | "group" | "shield" | "generic";

// kindIcon picks the row icon from the registry key's family (audit item 22),
// so an admin alert about an event looks like an event. A task still waiting
// (group.pending) shows the group icon; only the decision reads as done.
export function kindIcon(kind: string): KindIcon {
  if (kind === "group.approved") return "check";
  if (/^(event|occurrence|registration|waitlist)\./.test(kind)) return "calendar";
  if (/^(account|add|household)\./.test(kind)) return "person";
  if (/^(membership|group)\./.test(kind)) return "group";
  if (/^(teen_consent|contact|guardian|consent)\./.test(kind)) return "shield";
  return "generic";
}
