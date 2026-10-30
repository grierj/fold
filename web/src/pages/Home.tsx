import { useEffect, useRef, useState } from "react";
import { Link, useLocation, useSearchParams } from "react-router-dom";
import { Events, Groups, Site } from "../api";
import type { EventOccurrence, GroupSummary } from "../types";
import { DateBadge } from "../components/DateBadge";
import { GroupCard } from "../components/GroupCard";
import { ComingUp } from "../components/ComingUp";
import { formatTime, greeting } from "../lib/when";
import { errorText } from "../lib/errors";
import { ErrorNotice } from "../components/ErrorNotice";
import { SearchBox } from "../components/ui/SearchBox";
import { EmptyState } from "../components/ui/EmptyState";
import { Illustration } from "../components/ui/Illustration";
import { Skeleton } from "../components/ui/Skeleton";
import { ButtonLink } from "../components/ui/Button";
import { useAuth } from "../auth";

function tagsFromParams(params: URLSearchParams): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of params.getAll("tag")) {
    const tag = raw.trim();
    if (!tag || seen.has(tag)) continue;
    seen.add(tag);
    out.push(tag);
  }
  return out;
}


// Separator before the i-th of n tags: "a or b", "a, b, or c".
function orSeparator(i: number, n: number): string {
  if (i === 0) return "";
  if (n === 2) return " or ";
  return i === n - 1 ? ", or " : ", ";
}

export function Home() {
  const { user, loading: authLoading } = useAuth();
  const [retry, setRetry] = useState(0);
  const [searchParams, setSearchParams] = useSearchParams();
  const selectedTags = tagsFromParams(searchParams);
  const tagKey = selectedTags.join("\u0000");
  const [groups, setGroups] = useState<GroupSummary[]>([]);
  const [search, setSearch] = useState("");
  const [debounced, setDebounced] = useState("");
  // Chips list every tag in the directory the viewer can see, fetched once
  // without filters, so a link that opens with tags already selected still
  // offers all the others. Selected tags are always shown too.
  const [tagChoices, setTagChoices] = useState<string[]>([]);
  // When the page opens with no tag selected, the first (unfiltered) group
  // list doubles as the chip source, so it isn't fetched twice.
  const choicesFromList = useRef(tagKey === "");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [openEvents, setOpenEvents] = useState<EventOccurrence[]>([]);
  const { hash } = useLocation();
  // The signed-out front door names the church and, when an admin turned
  // self-signup off, offers Log in instead of Request an account (DEC-94).
  const [site, setSite] = useState<{ name: string; selfSignup: "request" | "off" } | null>(null);

  function toggleTag(tag: string) {
    const current = tagsFromParams(searchParams);
    const next = new URLSearchParams(searchParams);
    next.delete("tag");
    const same = (t: string) => t.toLowerCase() === tag.toLowerCase();
    const turningOff = current.some(same);
    for (const t of current) {
      if (!same(t)) next.append("tag", t);
    }
    if (!turningOff) next.append("tag", tag);
    setSearchParams(next, { replace: true });
  }

  function clearTags() {
    const next = new URLSearchParams(searchParams);
    next.delete("tag");
    setSearchParams(next, { replace: true });
  }

  useEffect(() => {
    const t = setTimeout(() => setDebounced(search), 300);
    return () => clearTimeout(t);
  }, [search]);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setError(null);
    const tags = tagKey ? tagKey.split("\u0000") : [];
    Groups.list(debounced, tags)
      .then(({ groups }) => {
        if (choicesFromList.current && tags.length === 0 && debounced === "") {
          choicesFromList.current = false;
          setTagChoices(groups.flatMap((g) => g.tags));
        }
        if (!alive) return;
        setGroups(groups);
      })
      .catch((e) => {
        if (alive) setError(errorText(e, "We couldn't load the groups."));
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [debounced, tagKey, retry]);

  useEffect(() => {
    if (user) return;
    let alive = true;
    Site.get()
      .then((s) => {
        if (alive) setSite({ name: s.name, selfSignup: s.selfSignup });
      })
      .catch(() => {
        if (alive) setSite({ name: "", selfSignup: "request" });
      });
    return () => {
      alive = false;
    };
  }, [user]);

  // Church-wide dates reach a signed-in person through Coming up, so this
  // block is only fetched for signed-out visitors.
  useEffect(() => {
    if (user) {
      setOpenEvents([]);
      return;
    }
    let alive = true;
    Events.listOpen()
      .then(({ events }) => {
        if (alive) setOpenEvents(events);
      })
      .catch(() => {
        if (alive) setOpenEvents([]);
      });
    return () => {
      alive = false;
    };
  }, [user]);

  // The router doesn't scroll to a hash, and #groups only exists once this
  // page has rendered, so do it here (the My groups link, DEC-94).
  useEffect(() => {
    if (hash !== "#groups" || loading) return;
    document.getElementById("groups")?.scrollIntoView?.({ block: "start" });
  }, [hash, loading]);

  useEffect(() => {
    if (choicesFromList.current) return;
    let alive = true;
    Groups.list()
      .then(({ groups }) => {
        if (alive) setTagChoices(groups.flatMap((g) => g.tags));
      })
      .catch(() => {
        // Chips are a convenience; the selected ones still show.
      });
    return () => {
      alive = false;
    };
  }, []);

  // Tags match case-insensitively, so chips do too: one chip per tag.
  const selectedLower = selectedTags.map((t) => t.toLowerCase());
  const byLower = new Map<string, string>();
  for (const t of [...selectedTags, ...tagChoices]) {
    if (!byLower.has(t.toLowerCase())) byLower.set(t.toLowerCase(), t);
  }
  const chips = [...byLower.values()].sort((a, b) => a.localeCompare(b));
  const clearTagsButton = (
    <button onClick={clearTags} className="font-semibold text-primary hover:underline rounded-sm">
      Clear the tags
    </button>
  );

  const filters = (
    <>
      <div className="max-w-xl">
        <SearchBox id="group-search" label="Search groups" value={search} onChange={setSearch} />
      </div>
      {chips.length > 0 && (
        <div className="mt-4 flex flex-wrap gap-2">
          <button onClick={clearTags} aria-pressed={selectedTags.length === 0} className="chip">
            All
          </button>
          {chips.map((t) => {
            const on = selectedLower.includes(t.toLowerCase());
            return (
              <button
                key={t}
                onClick={() => toggleTag(t)}
                aria-pressed={on}
                className="chip"
              >
                {/* .chip draws the checkmark on selected tags; once any
                    tag is on, the others show "+" to say they add more groups. */}
                {!on && selectedTags.length > 0 ? (
                  <span aria-hidden="true" className="text-text-subtle">
                    +
                  </span>
                ) : null}
                {t}
              </button>
            );
          })}
        </div>
      )}
      {selectedTags.length > 0 && (
        <p className="mt-3 text-sm text-text-muted" aria-live="polite">
          Showing groups tagged{" "}
          {selectedTags.map((t, i) => (
            <span key={t}>
              {orSeparator(i, selectedTags.length)}
              <strong className="font-semibold text-text">{t}</strong>
            </span>
          ))}
          .{" "}
          <button
            onClick={clearTags}
            className="font-semibold text-primary hover:underline rounded-sm"
          >
            Clear
          </button>
        </p>
      )}
    </>
  );

  // "Fold" is the default name, not a church's (the header treats it the same way).
  const church = site?.name && site.name !== "Fold" ? site.name : "";
  // A greeting uses the name a person goes by: their display name, else the
  // first word of their full name ("Good morning, Sam"), never the email.
  const name = user ? user.displayName || (user.name ?? "").trim().split(/\s+/)[0] : "";

  return (
    <div>
      {user ? (
        <section className="max-w-6xl mx-auto px-4 pt-8 space-y-8">
          <h1 className="text-3xl sm:text-4xl font-bold tracking-tight">
            {name ? `${greeting()}, ${name}` : "Welcome back"}
          </h1>
          <ComingUp />
        </section>
      ) : (
        <section className="bg-surface border-b border-border">
          <div className="max-w-6xl mx-auto px-4 py-12 sm:py-16">
            {church && <p className="mb-2 text-sm font-semibold uppercase tracking-wide text-primary">Welcome to {church}</p>}
            <h1 className="text-4xl sm:text-5xl font-bold tracking-tight">
              Find your people. Grow in faith together.
            </h1>
            <p className="mt-3 text-lg text-text-muted max-w-2xl">
              Small groups, ministries, and activities — all in one place. Join a group and show
              up.
            </p>
            {!authLoading && site && (
              <div className="mt-6 flex max-w-xl flex-col gap-3 sm:flex-row">
                {site.selfSignup === "off" ? (
                  <ButtonLink variant="primary" to="/login" layout="w-full sm:w-auto">Log in</ButtonLink>
                ) : (
                  <ButtonLink variant="primary" to="/join" layout="w-full sm:w-auto">Request an account</ButtonLink>
                )}
                <ButtonLink variant="secondary" to="/calendar" layout="w-full sm:w-auto">Browse public events</ButtonLink>
              </div>
            )}
            <div className="mt-6">{filters}</div>
          </div>
        </section>
      )}

      {openEvents.length > 0 && (
        <section className="max-w-6xl mx-auto px-4 pt-8">
          <h2 className="text-2xl font-bold mb-4">Church events</h2>
          <div className="space-y-3">
            {openEvents.map((ev) => (
              <Link
                key={`${ev.eventId}-${ev.date}`}
                to={ev.dateUrl || `/events/${ev.eventId}`}
                className="bg-surface rounded-lg border border-border p-4 flex gap-4 hover:border-border-strong"
              >
                <DateBadge at={ev.startsAt} />
                <div className="min-w-0">
                  <div className="font-bold text-lg">{ev.title || "Church event"}</div>
                  <div className="text-sm text-text-muted">
                    {formatTime(ev.startsAt)}
                    {ev.location ? ` · ${ev.location}` : ""}
                  </div>
                  {ev.description && <div className="text-sm text-text-muted line-clamp-2">{ev.description}</div>}
                </div>
              </Link>
            ))}
          </div>
        </section>
      )}

      {/* Group grid */}
      <section id="groups" className="max-w-6xl mx-auto px-4 py-8 scroll-mt-[var(--fold-header-height,4rem)]">
        <h2 className="text-2xl font-bold mb-4">Groups</h2>
        {/* Signed in, the hero is gone, so search and tags sit with the list. */}
        {user && <div className="mb-6">{filters}</div>}
        {/* The skeleton is for the first load only: while a new search or tag
            loads, the last results stay put instead of blinking out. */}
        {loading && groups.length === 0 && (
          <div className="grid gap-5 sm:grid-cols-2 lg:grid-cols-3">
            <Skeleton kind="cards" count={3} label="Loading groups…" />
          </div>
        )}
        {!loading && error && <ErrorNotice message={error} onRetry={() => setRetry((n) => n + 1)} />}
        {!loading && !error && groups.length === 0 && (
          selectedTags.length > 0 && debounced.trim() ? (
            <EmptyState
              title={`No groups match “${debounced.trim()}”`}
              body={
                <>
                  None of them have these tags. Try another search, or {clearTagsButton}.
                </>
              }
            />
          ) : debounced.trim() ? (
            <EmptyState
              title={`No groups match “${debounced.trim()}”`}
              body="Try a shorter word, or check the spelling."
            />
          ) : selectedTags.length > 0 ? (
            <EmptyState
              title="No groups match these tags"
              body={
                <>
                  Try another tag, or {clearTagsButton}.
                </>
              }
            />
          ) : (
            <EmptyState
              illustration={<Illustration name="first-group" />}
              title="No groups yet"
              body="Groups will show up here once someone starts one."
              action={
                user ? (
                  <ButtonLink to="/groups/new" variant="primary" layout="w-full sm:w-auto">
                    Start a group
                  </ButtonLink>
                ) : (
                  <ButtonLink to="/login" variant="secondary" layout="w-full sm:w-auto">
                    Log in
                  </ButtonLink>
                )
              }
            />
          )
        )}
        {!error && groups.length > 0 && (
          <div className="grid gap-5 sm:grid-cols-2 lg:grid-cols-3">
            {groups.map((g) => (
              <GroupCard key={g.id} group={g} />
            ))}
          </div>
        )}
      </section>
    </div>
  );
}
