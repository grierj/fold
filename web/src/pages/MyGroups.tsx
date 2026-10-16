import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Groups } from "../api";
import type { GroupSummary } from "../types";
import { GroupCard } from "../components/GroupCard";
import { useAuth } from "../auth";
import { errorText } from "../lib/errors";
import { ErrorNotice } from "../components/ErrorNotice";

// The server exposes GET /api/v1/me/groups for this.
export function MyGroups() {
  const { user, loading: authLoading } = useAuth();
  const navigate = useNavigate();
  const [groups, setGroups] = useState<GroupSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (authLoading) return;
    if (!user) {
      setLoading(false);
      return;
    }
    let alive = true;
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
  }, [user, authLoading]);

  if (!authLoading && !user) {
    return (
      <div className="max-w-6xl mx-auto px-4 py-12 text-center">
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
      {loading && <div className="text-text-muted">Loading…</div>}
      {error && <ErrorNotice message={error} onRetry={() => window.location.reload()} />}
      {!loading && !error && groups.length === 0 && (
        <div className="bg-surface rounded-lg border border-border p-8 text-center text-text-muted">
          You haven't joined any groups yet.{" "}
          <Link to="/" className="text-primary font-semibold">
            Find one to join
          </Link>
          .
        </div>
      )}
      <div className="grid gap-5 sm:grid-cols-2 lg:grid-cols-3">
        {groups.map((g) => (
          <GroupCard
            key={g.id}
            group={g}
            onTagClick={(tag) => navigate(`/?tag=${encodeURIComponent(tag)}`)}
          />
        ))}
      </div>
    </div>
  );
}
