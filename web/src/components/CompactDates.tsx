import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { EventOccurrence } from "../types";
import { placeLine } from "../lib/place";
import { dateLine } from "../lib/repeat";
import { usePage } from "../lib/usePage";
import { Button } from "./ui/Button";
import { PageCount, Pagination } from "./ui/Pagination";
import { SearchBox } from "./ui/SearchBox";
import { EmptyState } from "./ui/EmptyState";

// Small schedules keep direct answers. Longer schedules share one compact,
// searchable date list. Opened forms stay mounted while hidden, so browsing
// another date/page doesn't discard a guest form or RSVP draft.
export function CompactDates({ dates, requested, requestVersion = 0, loadedOnly = false, noun = "dates", children }: {
  dates: EventOccurrence[]; requested?: string; requestVersion?: number; loadedOnly?: boolean; noun?: string;
  children: (date: EventOccurrence) => ReactNode;
}) {
  const [query, setQuery] = useState("");
  const [expanded, setExpanded] = useState<number | null>(null);
  const handledRequest = useRef("");
  const [visited, setVisited] = useState<Set<number>>(() => new Set());
  const dense = dates.length > 6;
  const labels = useMemo(() => new Map(dates.map((d) => {
    const when = dateLine(d.startsAt, d.endsAt), place = placeLine(d.room, d.location);
    return [d.id, { when, place, search: `${d.title} ${d.date} ${when} ${place}`.toLowerCase() }] as const;
  })), [dates]);
  const matching = dates.filter((d) => labels.get(d.id)!.search.includes(query.trim().toLowerCase()));
  const paging = usePage(matching, query, 12);
  const visible = new Set(paging.rows.map((d) => d.id));
  const open = expanded ?? dates[0]?.id;
  useEffect(() => {
    if (!dense) setVisited((old) => dates.every((d) => old.has(d.id)) ? old : new Set([...old, ...dates.map((d) => d.id)]));
  }, [dense, dates]);
  useEffect(() => {
    const key = `${requested}:${requestVersion}`;
    if (!requested || handledRequest.current === key) return;
    if (query) { setQuery(""); return; }
    const index = dates.findIndex((d) => d.date === requested);
    if (index < 0) return;
    handledRequest.current = key;
    setExpanded(dates[index].id);
    setVisited((old) => new Set(old).add(dates[index].id));
    paging.go(Math.floor(index / 12) + 1);
  }, [requested, requestVersion, query, dates, paging]);
  return <div className={dense ? undefined : "space-y-4"}>
    {dense && <>
    <SearchBox label={loadedOnly ? "Search loaded events or dates" : "Search events or dates"} value={query} onChange={setQuery} />
    {loadedOnly && <p className="text-sm text-text-muted mt-2">Search covers the dates loaded below. Load more dates to search further ahead.</p>}
    <PageCount paging={paging} noun={noun} size={12} />
    {matching.length === 0 && <EmptyState title="No matching dates" body="Try an event name or a date." />}
    </>}
    {dates.map((d) => {
      const opened = open === d.id;
      const { when, place } = labels.get(d.id)!;
      return <div key={d.id} hidden={dense && !visible.has(d.id)} className={dense ? "border-b border-border py-3" : undefined}>
        {dense && <div className="flex items-start justify-between gap-3">
          <div className="min-w-0 break-words">
            <p className="font-semibold">{d.title || "Event"}</p>
            <p className="text-sm text-text-muted">{when}</p>
            {place && <p className="text-sm text-text-muted">{place}</p>}
            {d.myRegistration && <p className="text-sm font-medium">{d.myRegistration === "going" ? "Going" : d.myRegistration === "maybe" ? "Maybe" : d.myRegistration === "waitlisted" ? "Waitlisted" : d.myRegistration === "declined" ? "Can't go" : "Not answered"}</p>}
          </div>
          <Button variant="secondary" size="sm" layout="shrink-0 whitespace-nowrap" aria-expanded={opened} aria-label={`${opened ? "Hide details" : "Details and RSVP"}: ${d.title || "Event"}, ${when}`} aria-controls={`date-details-${d.id}`} onClick={() => {
            setExpanded(opened ? -1 : d.id);
            setVisited((old) => new Set(old).add(d.id));
          }}>{opened ? "Hide details" : "Details and RSVP"}</Button>
        </div>}
        <div id={`date-details-${d.id}`} hidden={dense && !opened}>
          {(!dense || opened || visited.has(d.id) || d.id === dates[0]?.id) && children(d)}
        </div>
      </div>;
    })}
    {dense && <Pagination paging={paging} noun={noun} />}
  </div>;
}
