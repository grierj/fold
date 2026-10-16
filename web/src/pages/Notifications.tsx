import { useSearchParams } from "react-router-dom";
import { FeedBody, useArchiveToast } from "../components/Notifications";
import { useAuth } from "../auth";
import { useFeed } from "../lib/notifications";
import type { NotificationFilter } from "../types";

const FILTERS: { key: NotificationFilter; label: string; empty?: string }[] = [
  { key: "all", label: "All" },
  { key: "unread", label: "Unread", empty: "Nothing unread." },
  { key: "waiting", label: "Waiting", empty: "Nothing is waiting on you." },
  { key: "archived", label: "Archived", empty: "Nothing archived." },
];

// /notifications: the full page, and where the bell goes on phones (6.10).
// The Archived filter is the durable Undo.
export function NotificationsPage() {
  const [params, setParams] = useSearchParams();
  const { user, isAdmin } = useAuth();
  const filter = (FILTERS.find((f) => f.key === params.get("filter"))?.key ?? "all") as NotificationFilter;
  const feed = useFeed(filter, !!user);
  const { onArchived, toast } = useArchiveToast(feed);
  if (!user) return null;
  // The Waiting chip shows to admins and to anyone with something waiting.
  const showWaiting = isAdmin || feed.waitingTotal > 0 || filter === "waiting";
  return (
    <div className="max-w-2xl mx-auto px-0 sm:px-4 py-4">
      <div className="sticky top-16 z-5 bg-surface border-b border-border px-4 py-3">
        <div className="flex items-center gap-3">
          <h1 className="text-xl font-bold mr-auto">Notifications</h1>
          {filter !== "archived" && (
            <button className="text-sm text-primary font-semibold hover:underline" title="Items waiting on you stay until they're handled" onClick={feed.readAll}>
              Mark all as read
            </button>
          )}
        </div>
        <div className="mt-2 flex flex-wrap gap-2" role="group" aria-label="Show">
          {FILTERS.filter((f) => f.key !== "waiting" || showWaiting).map((f) => (
            <button
              key={f.key}
              aria-pressed={filter === f.key}
              onClick={() => setParams(f.key === "all" ? {} : { filter: f.key })}
              className="chip"
            >
              {f.label}
            </button>
          ))}
        </div>
      </div>
      <div className="bg-surface sm:rounded-b-lg sm:border sm:border-t-0 border-border">
        <FeedBody feed={feed} onArchived={onArchived} dayHeadings emptyText={FILTERS.find((f) => f.key === filter)?.empty} />
      </div>
      {toast}
    </div>
  );
}
