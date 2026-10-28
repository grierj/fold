import { useEffect, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { Groups } from "../api";
import type { GroupSummary } from "../types";
import { GroupCard } from "../components/GroupCard";
import { useAuth } from "../auth";
import { errorText } from "../lib/errors";
import { ErrorNotice } from "../components/ErrorNotice";
import { EmptyState } from "../components/ui/EmptyState";
import { Illustration } from "../components/ui/Illustration";
import { Skeleton } from "../components/ui/Skeleton";
import { ButtonLink } from "../components/ui/Button";

// The server exposes GET /api/v1/me/groups for this.
export function MyGroups() {
  const { user, loading: authLoading } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const departureStatus = (location.state as { departureStatus?: string } | null)?.departureStatus;
  const [groups, setGroups] = useState<GroupSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [retry, setRetry] = useState(0);

  useEffect(() => {
    if (authLoading) return;
    if (!user) {
      setLoading(false);
      return;
    }
    let alive = true;
    setLoading(true);
    setError(null);
    (async () => {
      try {
        const { groups } = await Groups.mine();
        if (alive) {
          setGroups(groups);
        }
      } catch (e) {
        if (alive) setError(errorText(e, "We couldn't load your groups."));
      } finally {
        if (alive) setLoading(false);
      }
    })();
    return () => {
      alive = false;
    };
  }, [user, authLoading, retry]);

  if (!authLoading && !user) {
    return (
      <div className="max-w-6xl mx-auto px-4 py-12 text-center">
        <h1 className="text-3xl font-bold mb-6">My groups</h1>
        <p className="text-text-muted">
          <Link to="/login" className="text-primary font-semibold">
            Log in
          </Link>{" "}
          to see your groups.
        </p>
      </div>
    );
  }

  return (
    <div className="max-w-6xl mx-auto px-4 py-8">
      <h1 className="text-3xl font-bold mb-6">My groups</h1>
      {departureStatus && <p role="status" className="mb-4 rounded-lg border border-border bg-sunken px-4 py-3 text-sm">{departureStatus}</p>}
      {loading && (
        <div className="grid gap-5 sm:grid-cols-2 lg:grid-cols-3">
          <Skeleton kind="cards" count={3} />
        </div>
      )}
      {!loading && error && <ErrorNotice message={error} onRetry={() => setRetry((n) => n + 1)} />}
      {!loading && !error && groups.length === 0 && (
        <EmptyState
          illustration={<Illustration name="empty-groups" />}
          title="You're not in any groups yet"
          body="Groups you join show up here, with their next event."
          action={
            <>
              <ButtonLink to="/" variant="primary" layout="w-full sm:w-auto">
                Browse groups
              </ButtonLink>
              <ButtonLink to="/groups/new" variant="secondary" layout="w-full sm:w-auto">
                Start a group
              </ButtonLink>
            </>
          }
        />
      )}
      {!loading && groups.length > 0 && (
        <div className="grid gap-5 sm:grid-cols-2 lg:grid-cols-3">
          {groups.map((g) => (
            <GroupCard
              key={g.id}
              group={g}
              headingLevel={2}
              onTagClick={(tag) => navigate(`/?tag=${encodeURIComponent(tag)}`)}
            />
          ))}
        </div>
      )}
    </div>
  );
}
