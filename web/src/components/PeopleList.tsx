import { cloneElement, useState, type ReactElement } from "react";
import { usePage } from "../lib/usePage";
import { SearchBox } from "./ui/SearchBox";
import { Field, Select } from "./ui/Field";
import { EmptyState } from "./ui/EmptyState";
import { PageCount, Pagination } from "./ui/Pagination";

// Only presentation is shared. Callers retain their own authorized facts,
// identity/contact rules and mutation controls. Never search hidden identities.
export function PeopleList<T extends { name: string }>({ rows, noun, filters = [], sort = true, children }: {
  rows: T[]; noun: string; sort?: boolean;
  filters?: { value: string; label: string; matches: (row: T) => boolean }[];
  children: (row: T) => ReactElement<{ hidden?: boolean }>;
}) {
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("");
  const selected = filters.find((f) => f.value === filter);
  const ordered = sort ? [...rows].sort((a, b) => a.name.localeCompare(b.name)) : rows;
  const matching = ordered.filter((r) => r.name.toLowerCase().includes(query.trim().toLowerCase()) && (!selected || selected.matches(r)));
  const paging = usePage(matching, query + ":" + filter);
  const visible = new Set(paging.rows);
  return <div className="space-y-2">
    <div className="flex flex-wrap items-end gap-3">
      <div className="min-w-48 flex-1"><SearchBox label={`Search ${noun} by name`} value={query} onChange={setQuery} /></div>
      {filters.length > 0 && <div className="min-w-40"><Field label={`Filter ${noun}`}><Select value={filter} onChange={(e) => setFilter(e.target.value)} options={[{ value: "", label: "All" }, ...filters]} /></Field></div>}
    </div>
    <PageCount paging={paging} noun={noun} />
    {matching.length === 0 && <EmptyState title={rows.length ? "No one matches" : `No ${noun} yet`} body={rows.length ? "Try another name or choose another filter." : "People will appear here when they are added."} />}
    {/* Keep authorized rows mounted: a contact-request draft or submission must
        survive a page/filter change. Hidden rows are not keyboard targets. */}
    <ul className="fold-list">{ordered.map((row) => cloneElement(children(row), { hidden: !visible.has(row) }))}</ul>
    <Pagination paging={paging} noun={noun} />
  </div>;
}
