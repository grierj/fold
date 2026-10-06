import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Admin } from "../api";
import type { GroupSummary } from "../types";
import { useAuth } from "../auth";

function PendingCard({ group, onDone }: { group: GroupSummary; onDone: () => void }) {
  const [tags, setTags] = useState(group.tags.join(", "));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function approve() {
    setBusy(true);
    setError(null);
    try {
      await Admin.approve(
        group.id,
        tags
          .split(",")
          .map((t) => t.trim())
          .filter(Boolean)
      );
      onDone();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Approve failed");
    } finally {
      setBusy(false);
    }
  }

  async function reject() {
    if (!confirm(`Reject “${group.name}”? This cannot be undone.`)) return;
    setBusy(true);
    setError(null);
    try {
      await Admin.reject(group.id);
      onDone();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Reject failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="bg-white rounded-xl border border-gray-200 p-6">
      <div className="flex items-start gap-4 flex-wrap">
        <div className="grow min-w-0">
          <Link to={`/groups/${group.id}`} className="text-xl font-bold hover:text-brand">
            {group.name}
          </Link>
          <div className="text-sm text-gray-500 mt-0.5">
            Organized by {group.leaderName || "unknown"}
            {group.location ? ` · ${group.location}` : ""}
          </div>
          <p className="text-gray-700 mt-3 whitespace-pre-wrap">
            {group.description || "No description."}
          </p>
        </div>
      </div>
      <div className="mt-4">
        <label className="label">Tags</label>
        <input
          className="input"
          value={tags}
          onChange={(e) => setTags(e.target.value)}
          placeholder="e.g. young-adults, bible-study"
        />
      </div>
      {error && <div className="text-red-600 text-sm mt-2">{error}</div>}
      <div className="flex gap-3 mt-4">
        <button onClick={approve} disabled={busy} className="btn-primary">
          {busy ? "…" : "Approve"}
        </button>
        <button
          onClick={reject}
          disabled={busy}
          className="btn-secondary !text-red-600 !border-red-200 hover:!border-red-400"
        >
          Reject
        </button>
      </div>
    </div>
  );
}

export function AdminPage() {
  const { user, loading: authLoading, isAdmin } = useAuth();
  const [groups, setGroups] = useState<GroupSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const { groups } = await Admin.pending();
      setGroups(groups);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load approval queue");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!authLoading && user && isAdmin) load();
    else if (!authLoading) setLoading(false);
  }, [authLoading, user, isAdmin, load]);

  if (!authLoading && (!user || !isAdmin)) {
    return (
      <div className="max-w-4xl mx-auto px-4 py-12 text-center text-gray-600">
        This page is for church admins only.
      </div>
    );
  }

  return (
    <div className="max-w-4xl mx-auto px-4 py-8">
      <h1 className="text-3xl font-extrabold mb-6">Approval queue</h1>
      {loading && <div className="text-gray-500">Loading…</div>}
      {error && <div className="text-red-600">{error}</div>}
      {!loading && !error && groups.length === 0 && (
        <div className="bg-white rounded-xl border border-gray-200 p-8 text-center text-gray-600">
          Nothing pending. All caught up.
        </div>
      )}
      <div className="space-y-4">
        {groups.map((g) => (
          <PendingCard
            key={g.id}
            group={g}
            onDone={() => setGroups((prev) => prev.filter((x) => x.id !== g.id))}
          />
        ))}
      </div>
    </div>
  );
}
