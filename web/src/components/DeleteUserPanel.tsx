import { useEffect, useState } from "react";
import { AdminUsers } from "../api";
import { useAuth } from "../auth";
import type { SoleLedGroup, User } from "../types";

// Confirms deleting or disabling an account. For every group the person
// leads alone, the admin picks a new leader first: a member of that group or any admin, with
// the deleting admin preselected (DEC-36).
export function DeleteUserPanel({
  target,
  action,
  onDone,
}: {
  target: User;
  action: "delete" | "disable";
  onDone: (done: boolean) => void;
}) {
  const { user: me } = useAuth();
  const [groups, setGroups] = useState<SoleLedGroup[] | null>(null);
  const [picks, setPicks] = useState<Record<string, number>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    AdminUsers.soleLeaderGroups(target.id)
      .then(({ groups }) => {
        setGroups(groups);
        const initial: Record<string, number> = {};
        for (const g of groups) {
          const mine = g.candidates.find((c) => c.id === me?.id);
          const pick = mine ?? g.candidates[0];
          if (pick) initial[String(g.id)] = pick.id;
        }
        setPicks(initial);
      })
      .catch((e) => setError(e instanceof Error ? e.message : "Could not load groups"));
  }, [target.id, me?.id]);

  async function remove() {
    setBusy(true);
    setError(null);
    try {
      if (action === "delete") await AdminUsers.remove(target.id, picks);
      else await AdminUsers.update(target.id, { status: "disabled", successors: picks });
      onDone(true);
    } catch (e) {
      setError(e instanceof Error ? e.message : "That didn't work");
    } finally {
      setBusy(false);
    }
  }

  const name = target.displayName || target.name;
  return (
    <div className="mt-3 rounded-lg border border-red-200 bg-red-50 p-4 text-sm space-y-3">
      <p className="font-semibold text-red-800">
        {action === "delete" ? "Delete" : "Disable"} {name}? They will be signed out and can't sign in.
      </p>
      {groups === null && !error && <p className="text-gray-600">Checking their groups…</p>}
      {groups && groups.length > 0 && (
        <div className="space-y-2">
          <p className="text-gray-700">They are the only leader of these groups. Choose who leads each one next:</p>
          {groups.map((g) => (
            <label key={g.id} className="flex flex-wrap items-center gap-2">
              <span className="font-medium min-w-[10rem]">{g.name}</span>
              <select
                className="input !w-64"
                value={picks[String(g.id)] ?? ""}
                onChange={(e) => setPicks((p) => ({ ...p, [String(g.id)]: Number(e.target.value) }))}
              >
                {g.candidates.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.id === me?.id ? `Me (${c.name})` : c.name}
                    {c.admin && c.id !== me?.id ? " · admin" : ""}
                  </option>
                ))}
              </select>
            </label>
          ))}
        </div>
      )}
      {error && <div className="text-red-600">{error}</div>}
      <div className="flex gap-3">
        <button onClick={remove} disabled={busy || groups === null} className="btn-primary !bg-red-600">
          {busy ? "Working…" : action === "delete" ? "Delete account" : "Disable account"}
        </button>
        <button onClick={() => onDone(false)} className="btn-secondary">
          Cancel
        </button>
      </div>
    </div>
  );
}
