import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Groups } from "../api";
import type { GroupSummary } from "../types";
import { GroupCard } from "../components/GroupCard";

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

export function Home() {
  const [searchParams, setSearchParams] = useSearchParams();
  const selectedTags = tagsFromParams(searchParams);
  const tagKey = selectedTags.join("\u0000");
  const [groups, setGroups] = useState<GroupSummary[]>([]);
  const [search, setSearch] = useState("");
  const [debounced, setDebounced] = useState("");
  // Chips come from the last response that was not tag-filtered, so turning
  // tags on does not erase the others. Selected tags stay visible even when
  // the page was opened with them already set.
  const [tagChoices, setTagChoices] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  function toggleTag(tag: string) {
    const current = tagsFromParams(searchParams);
    const next = new URLSearchParams(searchParams);
    next.delete("tag");
    const turningOff = current.includes(tag);
    for (const t of current) {
      if (t !== tag) next.append("tag", t);
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
        if (!alive) return;
        setGroups(groups);
        if (tags.length === 0) {
          const s = new Set<string>();
          for (const g of groups) for (const t of g.tags) s.add(t);
          setTagChoices([...s].sort());
        }
      })
      .catch((e) => {
        if (alive) setError(e instanceof Error ? e.message : "Failed to load groups");
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [debounced, tagKey]);

  const chips = [...new Set([...tagChoices, ...selectedTags])].sort();

  return (
    <div>
      {/* Hero */}
      <section className="bg-white border-b border-gray-200">
        <div className="max-w-6xl mx-auto px-4 py-12 sm:py-16">
          <h1 className="text-4xl sm:text-5xl font-extrabold tracking-tight">
            Find your people. Grow in faith together.
          </h1>
          <p className="mt-3 text-lg text-gray-600 max-w-2xl">
            Small groups, ministries, and activities — all in one place. Join a group and show
            up.
          </p>
          <div className="mt-6 max-w-xl">
            <input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search groups…"
              className="input !py-3 text-lg shadow-sm"
            />
          </div>
          {chips.length > 0 && (
            <div className="mt-4 flex flex-wrap gap-2">
              <button
                onClick={clearTags}
                className={
                  selectedTags.length === 0
                    ? "px-3 py-1 rounded-full text-sm font-semibold bg-brand text-white"
                    : "px-3 py-1 rounded-full text-sm font-semibold bg-gray-100 text-gray-600 hover:bg-gray-200"
                }
              >
                All
              </button>
              {chips.map((t) => {
                const on = selectedTags.includes(t);
                return (
                  <button
                    key={t}
                    onClick={() => toggleTag(t)}
                    aria-pressed={on}
                    className={
                      on
                        ? "px-3 py-1 rounded-full text-sm font-semibold bg-brand text-white"
                        : "px-3 py-1 rounded-full text-sm font-semibold bg-gray-100 text-gray-600 hover:bg-gray-200"
                    }
                  >
                    {t}
                  </button>
                );
              })}
            </div>
          )}
        </div>
      </section>

      {/* Group grid */}
      <section className="max-w-6xl mx-auto px-4 py-8">
        <h2 className="text-2xl font-bold mb-4">Upcoming groups</h2>
        {loading && <div className="text-gray-500">Loading groups…</div>}
        {error && <div className="text-red-600">{error}</div>}
        {!loading && !error && groups.length === 0 && (
          <div className="bg-white rounded-xl border border-gray-200 p-8 text-center text-gray-600">
            No groups found. Try a different search or start a new group.
          </div>
        )}
        <div className="grid gap-5 sm:grid-cols-2 lg:grid-cols-3">
          {groups.map((g) => (
            <GroupCard
              key={g.id}
              group={g}
              selectedTags={selectedTags}
              onTagClick={toggleTag}
            />
          ))}
        </div>
      </section>
    </div>
  );
}
