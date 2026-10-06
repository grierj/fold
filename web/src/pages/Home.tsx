import { useEffect, useMemo, useState } from "react";
import { Groups } from "../api";
import type { GroupSummary } from "../types";
import { GroupCard } from "../components/GroupCard";

export function Home() {
  const [groups, setGroups] = useState<GroupSummary[]>([]);
  const [search, setSearch] = useState("");
  const [debounced, setDebounced] = useState("");
  const [tag, setTag] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const t = setTimeout(() => setDebounced(search), 300);
    return () => clearTimeout(t);
  }, [search]);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setError(null);
    Groups.list(debounced, tag)
      .then(({ groups }) => {
        if (alive) setGroups(groups);
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
  }, [debounced, tag]);

  const allTags = useMemo(() => {
    const s = new Set<string>();
    for (const g of groups) for (const t of g.tags) s.add(t);
    return [...s].sort();
  }, [groups]);

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
          {allTags.length > 0 && (
            <div className="mt-4 flex flex-wrap gap-2">
              <button
                onClick={() => setTag("")}
                className={
                  tag === ""
                    ? "px-3 py-1 rounded-full text-sm font-semibold bg-brand text-white"
                    : "px-3 py-1 rounded-full text-sm font-semibold bg-gray-100 text-gray-600 hover:bg-gray-200"
                }
              >
                All
              </button>
              {allTags.map((t) => (
                <button
                  key={t}
                  onClick={() => setTag(tag === t ? "" : t)}
                  className={
                    tag === t
                      ? "px-3 py-1 rounded-full text-sm font-semibold bg-brand text-white"
                      : "px-3 py-1 rounded-full text-sm font-semibold bg-gray-100 text-gray-600 hover:bg-gray-200"
                  }
                >
                  {t}
                </button>
              ))}
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
            <GroupCard key={g.id} group={g} />
          ))}
        </div>
      </section>
    </div>
  );
}
