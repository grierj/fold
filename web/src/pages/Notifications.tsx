import { useSearchParams } from "react-router-dom";
import { FeedBody, useArchiveToast } from "../components/Notifications";
import { Button, ButtonLink } from "../components/ui/Button";
import { Chip } from "../components/ui/Chip";
import { useAuth } from "../auth";
import { useBadge, useFeed } from "../lib/notifications";
import type { NotificationFilter } from "../types";

const FILTERS: { key: NotificationFilter; label: string; empty?: string }[] = [
  { key: "all", label: "All" },
  { key: "unread", label: "Unread", empty: "Nothing unread." },
  { key: "waiting", label: "Waiting", empty: "Nothing is waiting on you." },
  { key: "archived", label: "Archived", empty: "Nothing archived." },
];

// /notifications: the full page, and where the bell goes on phones (6.10).
// The Archived filter is the durable Undo. Laid out like the other pages: a
// title and chips on the page background, the list in one card (audit item 22).
export function NotificationsPage() {
  const [params, setParams] = useSearchParams();
  const { user, isAdmin } = useAuth();
  const filter = (FILTERS.find((f) => f.key === params.get("filter"))?.key ?? "all") as NotificationFilter;
  const feed = useFeed(filter, !!user);
  // The badge knows the true unread count even when the list is paged.
  // Waiting tasks aren't "unread": they stay until handled.
  const badge = useBadge(filter, !!user);
  const { onArchived, toast } = useArchiveToast(feed);
  if (!user) return null;
  // The Waiting chip shows to admins and to anyone with something waiting.
  const showWaiting = isAdmin || feed.waitingTotal > 0 || filter === "waiting";
  return (
    <div className="max-w-2xl mx-auto px-4 pb-8">
      <div className="sticky top-[var(--fold-header-height,4rem)] z-5 bg-page -mx-4 px-4 pt-6 pb-3">
        <div className="flex flex-wrap items-center gap-x-4">
          <h1 className="text-3xl font-bold mr-auto">Alerts</h1>
          <ButtonLink to="/profile/notifications" variant="link">
            Email settings
          </ButtonLink>
          {filter !== "archived" && (
            <Button
              variant="link"
              layout="disabled:text-text-muted disabled:no-underline"
              title="Items waiting on you stay until they're handled"
              disabled={badge.unread === 0}
              onClick={feed.readAll}
            >
              Mark all as read
            </Button>
          )}
        </div>
        <div className="mt-2 flex flex-wrap gap-2" role="group" aria-label="Show">
          {FILTERS.filter((f) => f.key !== "waiting" || showWaiting).map((f) => (
            <Chip key={f.key} selected={filter === f.key} onClick={() => setParams(f.key === "all" ? {} : { filter: f.key })}>
              {f.label}
            </Chip>
          ))}
        </div>
      </div>
      <div className="bg-surface border border-border rounded-lg overflow-hidden divide-y divide-border">
        <FeedBody
          feed={feed}
          onArchived={onArchived}
          dayHeadings
          emptyText={FILTERS.find((f) => f.key === filter)?.empty}
          emptyAction={
            <ButtonLink to="/calendar" variant="secondary" layout="w-full sm:w-auto">
              See what's coming up
            </ButtonLink>
          }
        />
      </div>
      {toast}
    </div>
  );
}
