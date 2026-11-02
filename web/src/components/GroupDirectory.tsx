import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { useLocation, useNavigationType, useSearchParams } from "react-router-dom";
import { Groups } from "../api";
import { useAuth } from "../auth";
import type { GroupSummary } from "../types";
import { errorText } from "../lib/errors";
import { useDirectoryPosition } from "../lib/directoryPosition";
import { GroupCard } from "./GroupCard";
import { ErrorNotice } from "./ErrorNotice";
import { Button, ButtonLink } from "./ui/Button";
import { Chip } from "./ui/Chip";
import { EmptyState } from "./ui/EmptyState";
import { Illustration } from "./ui/Illustration";
import { SearchBox } from "./ui/SearchBox";
import { Skeleton } from "./ui/Skeleton";

const BATCH = 12;
function tagsFromParams(params: URLSearchParams): string[] {
  const tags = new Map<string, string>();
  for (const raw of params.getAll("tag")) {
    const tag = raw.trim();
    if (tag && !tags.has(tag.toLowerCase())) tags.set(tag.toLowerCase(), tag);
  }
  return [...tags.values()];
}

export function GroupDirectory({ ready, jump, onJumped }: { ready: boolean; jump: boolean; onJumped: () => void }) {
  const { user } = useAuth();
  const [params, setParams] = useSearchParams();
  const location = useLocation();
  const navigation = useNavigationType();
  const position = useDirectoryPosition();
  const initial = useRef({ navigation, hash: location.hash });
  const restored = useRef(false);
  const section = useRef<HTMLElement>(null);
  const search = params.get("search") ?? "";
  const tags = tagsFromParams(params);
  const tagKey = tags.map((t) => t.toLowerCase()).join("\u0000");
  const requested = Number(params.get("shown"));
  const shown = Number.isSafeInteger(requested) && requested >= BATCH ? requested : BATCH;
  const [debounced, setDebounced] = useState(search);
  const [groups, setGroups] = useState<GroupSummary[]>([]);
  const [choices, setChoices] = useState<string[]>([]);
  const allGroups = useRef<ReturnType<typeof Groups.list> | null>(null);
  const firstResults = useRef(true);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [retry, setRetry] = useState(0);
  const [filtersOpen, setFiltersOpen] = useState(false);
  const filtersId = useId();
  const filtersButton = useRef<HTMLButtonElement>(null);
  const newResult = useRef<number | null>(null);
  const appendScroll = useRef<number | null>(null);
  const pending = loading || search !== debounced;
  const visible = groups.slice(0, shown);

  useEffect(() => {
    const timer = setTimeout(() => setDebounced(search), 300);
    return () => clearTimeout(timer);
  }, [search]);

  // Tag choices have a mount-scoped request, independent of a search that
  // might supersede the initial results before they arrive. Share that first
  // unfiltered request, but later result queries always get fresh data.
  useEffect(() => {
    let alive = true;
    const request = Groups.list();
    allGroups.current = request;
    request.then(({ groups }) => { if (alive) setChoices(groups.flatMap((g) => g.tags)); }).catch(() => {});
    return () => { alive = false; };
  }, []);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setError(null);
    const request = firstResults.current && !debounced && !tagKey && allGroups.current
      ? allGroups.current : Groups.list(debounced, tagKey ? tagKey.split("\u0000") : []);
    firstResults.current = false;
    request
      .then(({ groups }) => {
        if (!alive) return;
        setGroups(groups);
        if (!tagKey && !debounced) setChoices(groups.flatMap((g) => g.tags));
      })
      .catch((e) => { if (alive) setError(errorText(e, "We couldn't load the groups.")); })
      .finally(() => { if (alive) setLoading(false); });
    return () => { alive = false; };
  }, [debounced, tagKey, retry]);

  // Wait for the content ABOVE the directory as well as its results. A late
  // agenda must not move the restored row or a #groups jump off screen.
  useEffect(() => {
    if (!ready || pending) return;
    if (jump) {
      section.current?.scrollIntoView?.({ block: "start" });
      section.current?.querySelector<HTMLElement>("h2")?.focus({ preventScroll: true });
      onJumped();
      restored.current = true;
      return;
    }
    if (restored.current) return;
    restored.current = true;
    const saved = initial.current.navigation === "POP" ? position?.current : null;
    const row = saved?.query === location.search ? document.getElementById(`directory-group-${saved.groupId}`) : null;
    if (row && saved) {
      window.scrollTo({ top: Math.max(0, window.scrollY + row.getBoundingClientRect().top - saved.offset) });
      row.querySelector<HTMLAnchorElement>("a")?.focus({ preventScroll: true });
    } else if (initial.current.hash === "#groups") {
      section.current?.scrollIntoView?.({ block: "start" });
    }
  }, [ready, pending, jump, onJumped, position, location.search]);

  // Appending rows must not make browser scroll anchoring follow the button
  // down the page. Focus stays put; the next Tab visits the first added row.
  useLayoutEffect(() => {
    if (appendScroll.current === null) return;
    window.scrollTo({ top: appendScroll.current });
    appendScroll.current = null;
    // The final batch removes the button; give its keyboard focus a useful
    // destination instead of dropping it on the document body.
    if (shown >= groups.length && newResult.current !== null) {
      document.querySelector<HTMLAnchorElement>(`#directory-group-${newResult.current} a`)?.focus({ preventScroll: true });
      newResult.current = null;
    }
  }, [shown, groups.length]);

  function change(update: (next: URLSearchParams) => void) {
    newResult.current = null;
    setParams((previous) => {
      const next = new URLSearchParams(previous);
      update(next);
      next.delete("shown");
      return next;
    }, { replace: true });
  }
  function clearTags() { change((next) => next.delete("tag")); }
  function clearSelectedTags() {
    clearTags();
    filtersButton.current?.focus({ preventScroll: true });
  }
  function toggleTag(tag: string) {
    change((next) => {
      const current = tagsFromParams(next);
      const same = (t: string) => t.toLowerCase() === tag.toLowerCase();
      next.delete("tag");
      for (const t of current.filter((t) => !same(t))) next.append("tag", t);
      if (!current.some(same)) next.append("tag", tag);
    });
  }
  const byLower = new Map<string, string>();
  for (const tag of [...tags, ...choices]) if (!byLower.has(tag.toLowerCase())) byLower.set(tag.toLowerCase(), tag);
  const chips = [...byLower.values()].sort((a, b) => a.localeCompare(b));
  const clearTagsButton = <Button variant="link" onClick={clearSelectedTags}>Clear the tags</Button>;
  const count = visible.length < groups.length ? `Showing ${visible.length} of ${groups.length} groups` : `${groups.length} ${groups.length === 1 ? "group" : "groups"}`;

  return (
    <section id="groups" ref={section} aria-labelledby="directory-heading" className="min-w-0">
      <h2 id="directory-heading" tabIndex={-1} className="text-xl font-bold mb-2">Groups</h2>
      <div className="directory-toolbar sticky top-[var(--fold-header-height,4rem)] z-[5] border-b border-border bg-page py-3">
        <div className="flex items-center gap-3">
          <div className="min-w-0 flex-1"><SearchBox id="group-search" label="Search groups" value={search} onChange={(value) => change((next) => { if (value) next.set("search", value); else next.delete("search"); })} /></div>
          <Button ref={filtersButton} variant="secondary" aria-expanded={filtersOpen} aria-controls={filtersId} onClick={() => {
            setFiltersOpen(!filtersOpen);
            if (!filtersOpen) section.current?.scrollIntoView?.({ block: "start" });
          }}>
            Filters{tags.length > 0 ? ` (${tags.length})` : ""}
          </Button>
        </div>
        <p role="status" aria-atomic="true" className="mt-2 text-sm text-text-muted">{error ? "" : pending ? "Finding groups…" : count}</p>
      </div>
      {tags.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 py-3" aria-label="Selected group filters" role="group">
          <span className="text-sm text-text-muted">Any of:</span>
          {tags.map((tag) => <Chip key={tag.toLowerCase()} selected aria-label={`Remove ${tag} filter`} onClick={() => { toggleTag(tag); filtersButton.current?.focus({ preventScroll: true }); }}>{tag}</Chip>)}
          <Button variant="link" onClick={clearSelectedTags}>Clear</Button>
        </div>
      )}
      <div id={filtersId} hidden={!filtersOpen} className="border-b border-border py-4">
        <p className="text-sm text-text-muted mb-3">Choose tags to narrow the list. Groups matching any selected tag are shown.</p>
        <div className="flex flex-wrap gap-2" role="group" aria-label="Group tags">
          <Chip selected={tags.length === 0} onClick={clearTags}>All</Chip>
          {chips.map((tag) => <Chip key={tag.toLowerCase()} selected={tags.some((t) => t.toLowerCase() === tag.toLowerCase())} onClick={() => toggleTag(tag)}>{tag}</Chip>)}
        </div>
        <Button variant="secondary" layout="mt-4" onClick={() => { setFiltersOpen(false); section.current?.scrollIntoView?.({ block: "start" }); document.getElementById("group-search")?.focus({ preventScroll: true }); }}>Done with filters</Button>
      </div>
      {loading && groups.length === 0 && <Skeleton kind="lines" count={3} label="Loading groups…" />}
      {!pending && error && <ErrorNotice message={error} onRetry={() => setRetry((n) => n + 1)} />}
      {!pending && !error && groups.length === 0 && (
        tags.length > 0 && debounced.trim() ? <EmptyState title={`No groups match “${debounced.trim()}”`} body={<>None of them have these tags. Try another search, or {clearTagsButton}.</>} />
          : debounced.trim() ? <EmptyState title={`No groups match “${debounced.trim()}”`} body="Try a shorter word, or check the spelling." />
          : tags.length > 0 ? <EmptyState title="No groups match these tags" body={<>Try another tag, or {clearTagsButton}.</>} />
          : <EmptyState illustration={<Illustration name="first-group" />} title="No groups yet" body="Groups will show up here once someone starts one." action={<ButtonLink to={user ? "/groups/new" : "/login"} variant={user ? "primary" : "secondary"}>{user ? "Start a group" : "Log in"}</ButtonLink>} />
      )}
      {!error && groups.length > 0 && (
        <ul className="directory-results grid gap-x-8 lg:grid-cols-2" aria-busy={pending}>
          {visible.map((group, index) => (
            <li key={group.id} id={`directory-group-${group.id}`} className="min-w-0 border-b border-border" onClickCapture={(e) => {
              if (position && e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey) {
                position.current = { query: location.search, groupId: group.id, offset: e.currentTarget.getBoundingClientRect().top };
              }
            }}><GroupCard group={group} priority={index === 0} /></li>
          ))}
        </ul>
      )}
      {!error && visible.length < groups.length && (
        <Button variant="secondary" layout="mt-4 w-full sm:w-auto" disabled={pending} onClick={() => {
          newResult.current = groups[visible.length].id;
          appendScroll.current = window.scrollY;
          setParams((previous) => { const next = new URLSearchParams(previous); next.set("shown", String(Math.min(groups.length, shown + BATCH))); return next; }, { replace: true });
        }} onKeyDown={(e) => {
          if (e.key === "Tab" && !e.shiftKey && newResult.current !== null) {
            const next = document.querySelector<HTMLAnchorElement>(`#directory-group-${newResult.current} a`);
            if (next) { e.preventDefault(); next.focus(); newResult.current = null; }
          }
        }}>Show more groups</Button>
      )}
    </section>
  );
}
