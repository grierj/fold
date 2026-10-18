import { useEffect, useState } from "react";
import { AdminUsers } from "../api";
import { useAuth } from "../auth";
import type { SoleLedGroup, User } from "../types";
import { errorText } from "../lib/errors";
import { Button } from "./ui/Button";
import { Field, FormError, Select } from "./ui/Field";

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
      .catch((e) => setError(errorText(e, "We couldn't load the groups.")));
  }, [target.id, me?.id]);

  async function remove() {
    setBusy(true);
    setError(null);
    try {
      if (action === "delete") await AdminUsers.remove(target.id, picks);
      else await AdminUsers.update(target.id, { status: "disabled", successors: picks });
      onDone(true);
    } catch (e) {
      setError(errorText(e, "That didn't work."));
    } finally {
      setBusy(false);
    }
  }

  const name = target.displayName || target.name;
  return (
    <div className="mt-3 rounded-md border border-danger-border bg-danger-subtle p-4 text-sm space-y-3">
      <p className="font-semibold text-danger-on-subtle">
        {action === "delete" ? "Delete" : "Disable"} {name}? They will be signed out and can't sign in.
      </p>
      {action === "disable" && (
        <p className="text-text-muted">
          Their groups and registrations are kept but hidden until you enable them again; they come back as a member, not a
          leader.
        </p>
      )}
      {groups === null && !error && <p className="text-text-muted">Checking their groups…</p>}
      {groups && groups.length > 0 && (
        <div className="space-y-2">
          <p className="text-text-muted">They are the only leader of these groups. Choose who leads each one next:</p>
          {groups.map((g) => (
            <Field key={g.id} label={`Next leader of ${g.name}`}>
              <Select
                value={picks[String(g.id)] ?? ""}
                onChange={(e) => setPicks((p) => ({ ...p, [String(g.id)]: Number(e.target.value) }))}
                layout="sm:max-w-xs"
              >
                {g.candidates.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.id === me?.id ? `Me (${c.name})` : c.name}
                    {c.admin && c.id !== me?.id ? " · admin" : ""}
                  </option>
                ))}
              </Select>
            </Field>
          ))}
        </div>
      )}
      {error && <FormError>{error}</FormError>}
      <div className="flex gap-3">
        <Button variant="danger" onClick={remove} disabled={busy || groups === null}>
          {busy ? "Working…" : action === "delete" ? "Delete account" : "Disable account"}
        </Button>
        <Button variant="secondary" onClick={() => onDone(false)}>
          Cancel
        </Button>
      </div>
    </div>
  );
}
