import { useEffect, useState } from "react";
import { Link, useLocation, useSearchParams } from "react-router-dom";
import { Groups } from "../api";
import type { GroupSummary } from "../types";
import { usePage } from "../lib/usePage";
import { PageCount, Pagination } from "../components/ui/Pagination";
import { SearchBox } from "../components/ui/SearchBox";
import { GroupCard } from "../components/GroupCard";
import { useAuth } from "../auth";
import { errorText } from "../lib/errors";
import { ErrorNotice } from "../components/ErrorNotice";
import { EmptyState } from "../components/ui/EmptyState";
import { Illustration } from "../components/ui/Illustration";
import { Skeleton } from "../components/ui/Skeleton";
import { ButtonLink } from "../components/ui/Button";

// The server exposes GET /api/v1/me/groups for this.
// A person in no groups also sees up to three groups meeting soon (DEC-94):
// directory groups with a next meeting that they aren't in. The directory is
// already sorted soonest first, and a failure just hides the section.
const SOON_LIMIT = 3;

function MeetingSoon() {
  const [groups, setGroups] = useState<GroupSummary[] | null>(null);
  useEffect(() => {
    let alive = true;
    Groups.list()
      .then(({ groups }) => {
        if (alive) setGroups(groups.filter((g) => g.nextEventAt && !g.myRole).slice(0, SOON_LIMIT));
      })
      .catch(() => {
        if (alive) setGroups([]);
      });
    return () => {
      alive = false;
    };
  }, []);
  // Nothing is shown until we know something qualifies, so the heading never
  // flashes in and vanishes.
  if (!groups || groups.length === 0) return null;
  return (
    <section aria-labelledby="meeting-soon" className="fold-section mt-8">
      <h2 id="meeting-soon" className="text-xl font-bold mb-4">
        Groups meeting soon
      </h2>
      <div className="fold-list">
        {groups.map((g, index) => <GroupCard key={g.id} group={g} headingLevel={3} priority={index === 0} />)}
      </div>
    </section>
  );
}

export function MyGroups() {
  const { user, loading: authLoading } = useAuth();
  const location = useLocation();
  const [params, setParams] = useSearchParams();
  const query = params.get("search") ?? "";
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

  const matching = groups.filter((g) => `${g.name} ${g.description}`.toLowerCase().includes(query.trim().toLowerCase()));
  const paging = usePage(matching, query, 12, { page: Number(params.get("page")) || 1, onChange: (page) => setParams((old) => { const next = new URLSearchParams(old); next.set("page", String(page)); return next; }) });

  if (!authLoading && !user) {
    return (
      <div className="fold-page-narrow">
        <h1 className="fold-page-header text-3xl font-bold">My groups</h1>
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
    <div className="fold-page-narrow">
      <h1 className="fold-page-header text-3xl font-bold">My groups</h1>
      {departureStatus && <p role="status" className="mb-4 rounded-lg border border-border bg-sunken px-4 py-3 text-sm">{departureStatus}</p>}
      {loading && (
        <div className="fold-list">
          <Skeleton kind="lines" count={3} />
        </div>
      )}
      {!loading && error && <ErrorNotice message={error} onRetry={() => setRetry((n) => n + 1)} />}
      {!loading && !error && groups.length === 0 && (
        <>
          <EmptyState
            illustration={<Illustration name="empty-groups" />}
            title="You're not in any groups yet"
            body="Groups you join show up here, with their next event."
            action={
              <div className="flex w-full flex-col items-center gap-2 sm:w-auto">
                <ButtonLink to="/#groups" variant="primary" layout="w-full sm:w-auto">
                  Browse groups
                </ButtonLink>
                <p className="text-sm text-text-muted">
                  or{" "}
                  <Link to="/groups/new" className="text-link">
                    start a group
                  </Link>
                </p>
              </div>
            }
          />
          <MeetingSoon />
        </>
      )}
      {!loading && !error && groups.length > 0 && (
        <div>
          <SearchBox label="Search your groups" value={query} onChange={(value) => setParams((old) => { const next = new URLSearchParams(old); if (value) next.set("search", value); else next.delete("search"); next.delete("page"); return next; }, { replace: true })} />
          <PageCount paging={paging} noun="groups" size={12} />
          {matching.length === 0 && <EmptyState title="No groups match" body="Try another group name or a shorter search." />}
          <div className="fold-list">
          {paging.rows.map((g, index) => (
            <GroupCard key={g.id} group={g} headingLevel={2} showRole={false} priority={index === 0} />
          ))}
          </div>
          <Pagination paging={paging} noun="groups" />
        </div>
      )}
    </div>
  );
}
