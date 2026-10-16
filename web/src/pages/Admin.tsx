import { useCallback, useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Admin, AdminAccountRequests, AdminGroups, AdminUsers, ApiError, Groups, Invitations, Site } from "../api";
import { ZoneSelect } from "../components/ZoneSelect";
import type { AccountRequest, GroupSummary, Member, User } from "../types";
import { useAuth } from "../auth";
import { Avatar } from "../components/Avatar";
import { DeleteUserPanel } from "../components/DeleteUserPanel";
import { errorText } from "../lib/errors";

type AdminTab = "approvals" | "groups" | "users" | "invite";

// ---------------------------------------------------------------------------
// Approvals
// ---------------------------------------------------------------------------

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
      setError(errorText(e, "We couldn't approve that."));
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
      setError(errorText(e, "We couldn't reject that."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="bg-surface rounded-lg border border-border p-6">
      <div className="flex items-start gap-4 flex-wrap">
        <div className="grow min-w-0">
          <Link to={`/groups/${group.id}`} className="text-xl font-bold hover:text-primary">
            {group.name}
          </Link>
          <div className="text-sm text-text-muted mt-0.5">
            Organized by {group.leaderName || "unknown"}
            {group.location ? ` · ${group.location}` : ""}
          </div>
          <p className="text-text-muted mt-3 whitespace-pre-wrap">
            {group.description || "No description."}
          </p>
        </div>
      </div>
      <div className="mt-4">
        <label className="label" htmlFor="admin-tags">Tags</label>
        <input id="admin-tags"
          className="input"
          value={tags}
          onChange={(e) => setTags(e.target.value)}
          placeholder="e.g. young-adults, bible-study"
        />
      </div>
      {error && <div className="text-danger text-sm mt-2">{error}</div>}
      <div className="flex gap-3 mt-4">
        <button onClick={approve} disabled={busy} className="btn-primary">
          {busy ? "…" : "Approve"}
        </button>
        <button
          onClick={reject}
          disabled={busy}
          className="btn-danger-outline"
        >
          Reject
        </button>
      </div>
    </div>
  );
}

function DeletionCard({ group, onDone }: { group: GroupSummary; onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function approve() {
    if (!confirm(`Delete “${group.name}”? This removes the group, its members, and its events.`)) return;
    setBusy(true);
    setError(null);
    try {
      await Admin.deleteGroup(group.id);
      onDone();
    } catch (e) {
      setError(errorText(e, "We couldn't delete that."));
    } finally {
      setBusy(false);
    }
  }

  async function keep() {
    setBusy(true);
    setError(null);
    try {
      await Admin.rejectDeletion(group.id);
      onDone();
    } catch (e) {
      setError(errorText(e, "We couldn't keep the group."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="bg-surface rounded-lg border border-border p-6">
      <Link to={`/groups/${group.id}`} className="text-xl font-bold hover:text-primary">
        {group.name}
      </Link>
      <div className="text-sm text-text-muted mt-0.5">
        Organized by {group.leaderName || "unknown"} · {group.memberCount}{" "}
        {group.memberCount === 1 ? "member" : "members"}
      </div>
      <p className="text-text-muted mt-3">The leader asked for this group to be deleted.</p>
      {error && <div className="text-danger text-sm mt-2">{error}</div>}
      <div className="flex gap-3 mt-4">
        <button
          onClick={approve}
          disabled={busy}
          className="btn-danger-outline"
        >
          {busy ? "…" : "Delete group"}
        </button>
        <button onClick={keep} disabled={busy} className="btn-primary">
          Keep group
        </button>
      </div>
    </div>
  );
}

function AccountRequestCard({ request, onDone }: { request: AccountRequest; onDone: () => void }) {
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function decide(approve: boolean) {
    if (!approve && !confirm(`Reject the account request from ${request.name}? They'll be emailed.`)) return;
    setBusy(true);
    setError(null);
    try {
      if (approve) {
        await AdminAccountRequests.approve(request.id, note || undefined);
      } else {
        await AdminAccountRequests.reject(request.id, note || undefined);
      }
      onDone();
    } catch (e) {
      setError(errorText(e, "That didn't work."));
    } finally {
      setBusy(false);
    }
  }

  const warnings: string[] = [];
  if (request.accountType === "teen") warnings.push("Teen — needs consent after approval");
  if (request.warnings.duplicate) warnings.push("Duplicate pending request");
  if (request.warnings.honeypot) warnings.push("Honeypot filled");
  if (request.warnings.rateLimited) warnings.push("Many requests from one connection");

  return (
    <div className="bg-surface rounded-lg border border-border p-6">
      <div className="flex items-start gap-3 flex-wrap">
        <div className="grow min-w-0">
          <span className="text-xl font-bold">{request.name}</span>
          {request.accountType === "teen" && (
            <span className="ml-2 inline-block rounded-full bg-info-subtle px-2 py-0.5 text-xs font-semibold text-info-on-subtle">Teen</span>
          )}
          <div className="text-sm text-text-muted mt-0.5">
            {request.email}
            {request.phone ? ` · ${request.phone}` : ""}
            {request.birthDate ? ` · born ${request.birthDate}` : ""}
          </div>
          <div className="text-sm text-text-muted">
            {request.source === "member_invite"
              ? `Invited by ${request.requestedByName || "a member"}`
              : "Self-signup"}{" "}
            · asked {new Date(request.createdAt).toLocaleDateString()}
          </div>
          {request.message && <p className="text-text-muted mt-3 whitespace-pre-wrap">{request.message}</p>}
          {warnings.length > 0 && (
            <div className="mt-3 flex flex-wrap gap-1.5">
              {warnings.map((w) => (
                <span key={w} className="inline-block rounded-full bg-warning-subtle px-2 py-0.5 text-xs font-semibold text-warning-on-subtle">{w}</span>
              ))}
            </div>
          )}
        </div>
      </div>
      <div className="mt-4">
        <label className="label" htmlFor={`note-${request.id}`}>Note (optional, emailed on rejection)</label>
        <input id={`note-${request.id}`} className="input" value={note} onChange={(e) => setNote(e.target.value)} maxLength={500} />
      </div>
      {error && <div className="text-danger text-sm mt-2">{error}</div>}
      <div className="flex gap-3 mt-4">
        <button onClick={() => decide(true)} disabled={busy} className="btn-primary">{busy ? "…" : "Approve"}</button>
        <button onClick={() => decide(false)} disabled={busy} className="btn-danger-outline">Reject</button>
      </div>
    </div>
  );
}

function ApprovalsTab({ onCount }: { onCount: (n: number) => void }) {
  const [groups, setGroups] = useState<GroupSummary[]>([]);
  const [deletions, setDeletions] = useState<GroupSummary[]>([]);
  const [requests, setRequests] = useState<AccountRequest[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [pending, requested, accounts] = await Promise.all([
        Admin.pending(), Admin.deletions(), AdminAccountRequests.list(),
      ]);
      setGroups(pending.groups);
      setDeletions(requested.groups);
      setRequests(accounts.requests);
      onCount(pending.groups.length + requested.groups.length + accounts.requests.length);
    } catch (e) {
      setError(errorText(e, "We couldn't load the approval queue."));
    } finally {
      setLoading(false);
    }
  }, [onCount]);

  useEffect(() => {
    load();
  }, [load]);

  return (
    <div>
      {loading && <div className="text-text-muted">Loading…</div>}
      {error && <div className="text-danger">{error}</div>}
      {!loading && !error && groups.length === 0 && deletions.length === 0 && requests.length === 0 && (
        <div className="bg-surface rounded-lg border border-border p-8 text-center text-text-muted">
          Nothing pending. All caught up.
        </div>
      )}
      {requests.length > 0 && (
        <div className="space-y-4 mb-8">
          <h2 className="text-xl font-bold">Account requests</h2>
          {requests.map((r) => (
            <AccountRequestCard key={r.id} request={r} onDone={load} />
          ))}
        </div>
      )}
      {groups.length > 0 && (
        <div className="space-y-4">
          <h2 className="text-xl font-bold">New groups</h2>
          {groups.map((g) => (
            <PendingCard key={g.id} group={g} onDone={load} />
          ))}
        </div>
      )}
      {deletions.length > 0 && (
        <div className="space-y-4 mt-8">
          <h2 className="text-xl font-bold">Deletion requests</h2>
          {deletions.map((g) => (
            <DeletionCard key={g.id} group={g} onDone={load} />
          ))}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Groups: list, edit (via group page), manage members
// ---------------------------------------------------------------------------

function MemberManager({ group, users }: { group: GroupSummary; users: User[] }) {
  const [members, setMembers] = useState<Member[] | null>(null);
  const [addUserId, setAddUserId] = useState("");
  const [addRole, setAddRole] = useState<"member" | "leader">("member");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const { members } = await Groups.members(group.id);
      setMembers(members);
    } catch (e) {
      setError(errorText(e, "We couldn't load the members."));
    }
  }, [group.id]);

  useEffect(() => {
    load();
  }, [load]);

  async function add() {
    if (!addUserId) return;
    setBusy(true);
    setError(null);
    try {
      try {
        await AdminGroups.addMember(group.id, Number(addUserId), addRole);
      } catch (err) {
        // Full group: going over the limit is an explicit, audited choice (DEC-39).
        if (!(err instanceof ApiError) || err.status !== 409) throw err;
        if (!confirm(`“${group.name}” is full. Add this person anyway?`)) return;
        await AdminGroups.addMember(group.id, Number(addUserId), addRole, true);
      }
      setAddUserId("");
      await load();
    } catch (e) {
      setError(errorText(e, "We couldn't add that person."));
    } finally {
      setBusy(false);
    }
  }

  async function remove(m: Member) {
    if (!confirm(`Remove ${m.name} from “${group.name}”?`)) return;
    setBusy(true);
    setError(null);
    try {
      await AdminGroups.removeMember(group.id, m.id);
      await load();
    } catch (e) {
      setError(errorText(e, "We couldn't remove that."));
    } finally {
      setBusy(false);
    }
  }

  const memberIds = new Set((members ?? []).map((m) => m.id));
  const candidates = users.filter((u) => !memberIds.has(u.id));

  return (
    <div className="mt-3 border-t border-border pt-3">
      {error && <div className="text-danger text-sm mb-2">{error}</div>}
      <ul className="divide-y divide-border">
        {(members ?? []).map((m) => (
          <li key={m.id} className="py-2 flex items-center gap-3">
            <Avatar
              url={m.avatarUrl || undefined}
              name={m.name}
              focalX={m.avatarFocalX}
              focalY={m.avatarFocalY}
              size="w-8 h-8"
              textSize="text-xs"
            />
            <div className="grow min-w-0">
              <span className="font-medium">{m.name}</span>{" "}
              {m.admin && (
                <span className="mr-1 inline-block rounded-full bg-primary-subtle px-2 py-0.5 text-xs font-semibold text-primary-on-subtle">
                  Admin
                </span>
              )}
              <span className="text-xs text-text-muted capitalize">· {m.role}</span>
            </div>
            <button
              onClick={() => remove(m)}
              disabled={busy}
              className="text-sm text-danger hover:underline"
            >
              Remove
            </button>
          </li>
        ))}
      </ul>
      {members && members.length === 0 && (
        <p className="text-sm text-text-muted py-2">No members yet.</p>
      )}
      <div className="flex gap-2 mt-3 flex-wrap items-end">
        <div>
          <label className="label" htmlFor="admin-add-person">Add person</label>
          <select id="admin-add-person"
            className="input w-48!"
            value={addUserId}
            onChange={(e) => setAddUserId(e.target.value)}
          >
            <option value="">Select…</option>
            {candidates.map((u) => (
              <option key={u.id} value={u.id}>
                {u.displayName || u.name} ({u.email})
              </option>
            ))}
          </select>
        </div>
        <div>
          <label className="label" htmlFor="admin-role">Role</label>
          <select id="admin-role"
            className="input w-32!"
            value={addRole}
            onChange={(e) => setAddRole(e.target.value as "member" | "leader")}
          >
            <option value="member">Member</option>
            <option value="leader">Leader</option>
          </select>
        </div>
        <button onClick={add} disabled={busy || !addUserId} className="btn-secondary">
          {busy ? "…" : "Add"}
        </button>
      </div>
    </div>
  );
}

function GroupsTab({ users }: { users: User[] }) {
  const [groups, setGroups] = useState<GroupSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [openMembers, setOpenMembers] = useState<number | null>(null);
  const [deletingId, setDeletingId] = useState<number | null>(null);
  const [deleteError, setDeleteError] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      try {
        const { groups } = await AdminGroups.list();
        setGroups(groups);
      } catch (e) {
        setError(errorText(e, "We couldn't load the groups."));
      } finally {
        setLoading(false);
      }
    })();
  }, []);

  async function deleteGroup(g: GroupSummary) {
    if (!confirm(`Delete “${g.name}”? This removes the group, its members, and its events.`)) return;
    setDeletingId(g.id);
    setDeleteError(null);
    try {
      await Admin.deleteGroup(g.id);
      setGroups((prev) => prev.filter((x) => x.id !== g.id));
      setOpenMembers((v) => (v === g.id ? null : v));
    } catch (e) {
      setDeleteError(errorText(e, "We couldn't delete that."));
    } finally {
      setDeletingId(null);
    }
  }

  if (loading) return <div className="text-text-muted">Loading…</div>;
  if (error) return <div className="text-danger">{error}</div>;

  return (
    <div className="space-y-3">
      {deleteError && <div className="text-danger text-sm">{deleteError}</div>}
      {groups.map((g) => (
        <div key={g.id} className="bg-surface rounded-lg border border-border p-5">
          <div className="flex items-center gap-3 flex-wrap">
            <div className="grow min-w-0">
              <Link to={`/groups/${g.id}`} className="font-bold hover:text-primary">
                {g.name}
              </Link>
              <div className="text-xs text-text-muted mt-0.5">
                <span
                  className={`inline-block rounded-full px-2 py-0.5 font-semibold uppercase tracking-wide ${
                    g.status === "active"
                      ? "bg-success-subtle text-success-on-subtle"
                      : g.status === "pending"
                        ? "bg-warning-subtle text-warning-on-subtle"
                        : "bg-sunken text-text-muted"
                  }`}
                >
                  {g.status}
                </span>
                {g.visibility === "invite_only" && (
                  <span className="ml-1.5 inline-block rounded-full px-2 py-0.5 font-semibold uppercase tracking-wide bg-inverse text-inverse-foreground">
                    Invite only
                  </span>
                )}{" "}
                · {g.memberCount} members · {g.leaderName || "no leader"}
              </div>
            </div>
            <button
              onClick={() => setOpenMembers((v) => (v === g.id ? null : g.id))}
              className="btn-secondary py-1.5! px-3! text-sm"
            >
              {openMembers === g.id ? "Hide members" : "Manage members"}
            </button>
            <Link to={`/groups/${g.id}`} className="btn-secondary py-1.5! px-3! text-sm">
              Open & edit
            </Link>
            <button
              onClick={() => deleteGroup(g)}
              disabled={deletingId === g.id}
              className="btn-danger-outline py-1.5! px-3! text-sm"
            >
              {deletingId === g.id ? "…" : "Delete"}
            </button>
          </div>
          {openMembers === g.id && <MemberManager group={g} users={users} />}
        </div>
      ))}
      {groups.length === 0 && (
        <div className="bg-surface rounded-lg border border-border p-8 text-center text-text-muted">
          No groups yet.
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Users: list, create, edit, reset password, delete
// ---------------------------------------------------------------------------

const EMPTY_USER_FORM = {
  password: "",
  role: "member" as "admin" | "member",
  name: "",
  displayName: "",
  email: "",
  phone: "",
};

function UserForm({
  initial,
  creating,
  onSaved,
  onCancel,
}: {
  initial?: User;
  creating: boolean;
  onSaved: () => void;
  onCancel: () => void;
}) {
  const [form, setForm] = useState(
    initial
      ? {
          password: "",
          role: initial.role as "admin" | "member",
          name: initial.name,
          displayName: initial.displayName,
          email: initial.email,
          phone: initial.phone,
        }
      : { ...EMPTY_USER_FORM }
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) =>
    setForm((f) => ({ ...f, [k]: e.target.value }));

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      if (creating) {
        await AdminUsers.create({ ...form, password: form.password || undefined });
      } else if (initial) {
        const { password: _password, ...rest } = form;
        await AdminUsers.update(initial.id, rest);
      }
      onSaved();
    } catch (err) {
      setError(errorText(err, "We couldn't save your changes."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} className="bg-surface rounded-lg border border-border p-6 space-y-4 mt-4">
      {error && <div className="text-danger text-sm">{error}</div>}
      <div className="grid sm:grid-cols-2 gap-4">
        <div>
          <label className="label" htmlFor="admin-email">Email (signs in with it)</label>
          <input id="admin-email"
            type="email"
            className="input"
            value={form.email}
            onChange={set("email")}
            maxLength={254}
            required
          />
        </div>
        <div>
          <label className="label" htmlFor="admin-role-2">Role</label>
          <select id="admin-role-2" className="input" value={form.role} onChange={set("role")}>
            <option value="member">Member</option>
            <option value="admin">Admin</option>
          </select>
        </div>
        {creating && (
          <div className="sm:col-span-2">
            <label className="label" htmlFor="admin-password">Password (optional)</label>
            <input id="admin-password"
              type="password"
              className="input"
              value={form.password}
              onChange={set("password")}
              autoComplete="new-password"
            />
            <p className="text-xs text-text-muted mt-1">
              At least 10 characters. Leave blank to create an invited account that can't sign in until a password
              is set.
            </p>
          </div>
        )}
        <div>
          <label className="label" htmlFor="admin-display-name">Display name</label>
          <input id="admin-display-name" className="input" value={form.displayName} onChange={set("displayName")} maxLength={100} />
        </div>
        <div>
          <label className="label" htmlFor="admin-full-name">Full name</label>
          <input id="admin-full-name" className="input" value={form.name} onChange={set("name")} maxLength={100} required />
        </div>
        <div>
          <label className="label" htmlFor="admin-phone">Phone</label>
          <input id="admin-phone" type="tel" className="input" value={form.phone} onChange={set("phone")} maxLength={30} />
        </div>
      </div>
      <div className="flex gap-3">
        <button type="submit" disabled={busy} className="btn-primary">
          {busy ? "Saving…" : creating ? "Create user" : "Save changes"}
        </button>
        <button type="button" onClick={onCancel} className="btn-secondary">
          Cancel
        </button>
      </div>
    </form>
  );
}

function PasswordReset({ user, onDone }: { user: User; onDone: () => void }) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  async function reset(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await AdminUsers.resetPassword(user.id, password);
      setDone(true);
    } catch (err) {
      setError(errorText(err, "We couldn't reset the password."));
    } finally {
      setBusy(false);
    }
  }

  if (done) {
    return (
      <div className="text-sm text-success mt-2">
        Password set for {user.email}.{" "}
        <button onClick={onDone} className="underline">
          Done
        </button>
      </div>
    );
  }

  return (
    <form onSubmit={reset} className="flex gap-2 mt-2 flex-wrap items-end">
      <div>
        <label className="label" htmlFor="admin-new-password-for">New password for {user.email}</label>
        <input id="admin-new-password-for"
          type="password"
          className="input w-56!"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          required
        />
        <p className="text-xs text-text-muted mt-1">Must be at least 10 characters.</p>
      </div>
      <button type="submit" disabled={busy} className="btn-secondary py-2!">
        {busy ? "…" : "Set password"}
      </button>
      <button type="button" onClick={onDone} className="text-sm text-text-muted hover:underline">
        Cancel
      </button>
      {error && <div className="text-danger text-sm w-full">{error}</div>}
    </form>
  );
}

function UsersTab({ users, reload }: { users: User[]; reload: () => void }) {
  const [creating, setCreating] = useState(false);
  const [editingId, setEditingId] = useState<number | null>(null);
  const [resetId, setResetId] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function setStatus(u: User, status: "active" | "disabled") {
    if (status === "disabled" && !confirm(`Disable ${u.displayName || u.name}? They will be signed out and can't sign in.`)) return;
    setBusy(true);
    setError(null);
    try {
      await AdminUsers.update(u.id, { status });
      reload();
    } catch (e) {
      setError(errorText(e, "We couldn't update that."));
    } finally {
      setBusy(false);
    }
  }
  // Deleting or disabling opens a panel that also picks successors (DEC-36).
  const [leaving, setLeaving] = useState<{ id: number; action: "delete" | "disable" } | null>(null);

  return (
    <div>
      <div className="flex justify-end mb-4">
        <button onClick={() => { setCreating(true); setEditingId(null); }} className="btn-primary">
          New user
        </button>
      </div>
      {error && <div className="text-danger text-sm mb-3">{error}</div>}
      {creating && (
        <UserForm
          creating
          onSaved={() => { setCreating(false); reload(); }}
          onCancel={() => setCreating(false)}
        />
      )}
      <div className="bg-surface rounded-lg border border-border overflow-hidden">
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-text-muted border-b border-border">
              <th className="px-4 py-3 font-medium">User</th>
              <th className="px-4 py-3 font-medium hidden md:table-cell">Contact</th>
              <th className="px-4 py-3 font-medium">Role</th>
              <th className="px-4 py-3 font-medium text-right">Actions</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {users.map((u) => (
              <tr key={u.id}>
                <td className="px-4 py-3">
                  <div className="flex items-center gap-2">
                    <Avatar
                      url={u.avatarUrl || undefined}
                      name={u.displayName || u.name}
                      focalX={u.avatarFocalX}
                      focalY={u.avatarFocalY}
                      size="w-8 h-8"
                      textSize="text-xs"
                    />
                    <div>
                      <div className="font-medium">{u.displayName || u.name}</div>
                      <div className="text-xs text-text-muted">{u.email}</div>
                    </div>
                  </div>
                  {editingId === u.id && (
                    <UserForm
                      initial={u}
                      creating={false}
                      onSaved={() => { setEditingId(null); reload(); }}
                      onCancel={() => setEditingId(null)}
                    />
                  )}
                  {resetId === u.id && (
                    <PasswordReset user={u} onDone={() => setResetId(null)} />
                  )}
                  {leaving?.id === u.id && (
                    <DeleteUserPanel
                      target={u}
                      action={leaving.action}
                      onDone={(done) => { setLeaving(null); if (done) reload(); }}
                    />
                  )}
                </td>
                <td className="px-4 py-3 text-text-muted hidden md:table-cell">
                  {u.email || "—"}
                  {u.phone ? <div className="text-xs">{u.phone}</div> : null}
                </td>
                <td className="px-4 py-3">
                  <span
                    className={`inline-block rounded-full px-2 py-0.5 text-xs font-semibold ${
                      u.role === "admin" ? "bg-primary-subtle text-primary-on-subtle" : "bg-sunken text-text-muted"
                    }`}
                  >
                    {u.role}
                  </span>
                  {u.status !== "active" && (
                    <span className="ml-1 inline-block rounded-full px-2 py-0.5 text-xs font-semibold bg-warning-subtle text-warning-on-subtle">
                      {u.status}
                    </span>
                  )}
                  {u.accountType === "teen" && (
                    <span className="ml-1 inline-block rounded-full px-2 py-0.5 text-xs font-semibold bg-info-subtle text-info-on-subtle">
                      Teen
                    </span>
                  )}
                </td>
                <td className="px-4 py-3 text-right whitespace-nowrap">
                  <button
                    onClick={() => { setEditingId(u.id); setResetId(null); setCreating(false); }}
                    className="text-sm text-primary hover:underline mr-3"
                  >
                    Edit
                  </button>
                  <button
                    onClick={() => { setResetId(u.id); setEditingId(null); setCreating(false); }}
                    className="text-sm text-text-muted hover:underline mr-3"
                  >
                    Password
                  </button>
                  <button
                    onClick={() =>
                      u.status === "disabled"
                        ? setStatus(u, "active")
                        : setLeaving({ id: u.id, action: "disable" })
                    }
                    disabled={busy}
                    className="text-sm text-text-muted hover:underline mr-3"
                  >
                    {u.status === "disabled" ? "Enable" : "Disable"}
                  </button>
                  <button
                    onClick={() => { setLeaving({ id: u.id, action: "delete" }); setEditingId(null); setResetId(null); }}
                    disabled={busy}
                    className="text-sm text-danger hover:underline"
                  >
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

function ChurchTimezone() {
  const [tz, setTz] = useState("");
  const [saved, setSaved] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState(false);

  useEffect(() => {
    let alive = true;
    Site.get()
      .then((s) => {
        if (!alive) return;
        setTz(s.timezone);
        setSaved(s.timezone);
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, []);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setOk(false);
    try {
      const r = await Admin.setTimezone(tz.trim());
      setTz(r.timezone);
      setSaved(r.timezone);
      setOk(true);
    } catch (err) {
      setError(errorText(err, "We couldn't save the time zone."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} className="mb-6 flex flex-wrap items-end gap-3">
      <div className="w-full sm:w-auto sm:flex-1 sm:max-w-md">
        <label className="label" htmlFor="church-tz">
          Church time zone
        </label>
        <ZoneSelect
          id="church-tz"
          forChurchSetting
          value={tz}
          onChange={(v) => {
            setTz(v);
            setOk(false);
          }}
          describedBy="church-tz-hint"
        />
        <p id="church-tz-hint" className="text-sm text-text-muted mt-1">
          New events start in this time zone; leaders can pick another when they schedule.
        </p>
      </div>
      <button type="submit" className="btn-secondary" disabled={busy || tz.trim() === saved}>
        {busy ? "Saving…" : "Save time zone"}
      </button>
      {ok && <span className="text-sm text-text-muted">Saved</span>}
      {error && <span className="text-sm text-danger">{error}</span>}
    </form>
  );
}

// Invite someone to Fold (M1.2, 5.10): admins invite into any group or
// none. The person sets a password on acceptance; no approval step.
function InviteTab() {
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [groupId, setGroupId] = useState("");
  const [groups, setGroups] = useState<GroupSummary[]>([]);
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    Groups.list()
      .then((res) => setGroups(res.groups))
      .catch(() => {});
  }, []);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setStatus(null);
    try {
      await Invitations.create({
        email,
        name: name || undefined,
        groupId: groupId ? Number(groupId) : null,
      });
      setStatus(`Invitation sent to ${email}.`);
      setEmail("");
      setName("");
      setGroupId("");
    } catch (e) {
      setError(errorText(e, "We couldn't send the invitation."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} className="bg-surface rounded-lg border border-border p-6 space-y-4 max-w-xl">
      {error && <div className="text-danger text-sm">{error}</div>}
      {status && <div className="text-success text-sm">{status}</div>}
      <p className="text-sm text-text-muted">
        They'll get an email link, set a password, and join with an active account — no
        approval step.
      </p>
      <div>
        <label className="label" htmlFor="inv-email">
          Email
        </label>
        <input
          id="inv-email"
          type="email"
          className="input"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          required
        />
      </div>
      <div>
        <label className="label" htmlFor="inv-name">
          Name (optional)
        </label>
        <input
          id="inv-name"
          className="input"
          value={name}
          onChange={(e) => setName(e.target.value)}
          maxLength={100}
        />
      </div>
      <div>
        <label className="label" htmlFor="inv-group">
          Group (optional)
        </label>
        <select
          id="inv-group"
          className="input"
          value={groupId}
          onChange={(e) => setGroupId(e.target.value)}
        >
          <option value="">No group — they join Fold only</option>
          {groups.map((g) => (
            <option key={g.id} value={g.id}>
              {g.name}
            </option>
          ))}
        </select>
      </div>
      <button type="submit" disabled={busy} className="btn-primary">
        {busy ? "Sending…" : "Send invitation"}
      </button>
    </form>
  );
}

// Self-signup mode (DEC-4, M1.3): request (default) or off. There is
// no open mode; every request waits for an admin either way.
function SelfSignupSetting() {
  const [mode, setMode] = useState<"request" | "off">("request");
  const [saved, setSaved] = useState<"request" | "off">("request");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState(false);

  useEffect(() => {
    let alive = true;
    Site.get()
      .then((s) => {
        if (!alive) return;
        setMode(s.selfSignup);
        setSaved(s.selfSignup);
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, []);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setOk(false);
    try {
      const r = await AdminAccountRequests.setSelfSignup(mode);
      setMode(r.selfSignup);
      setSaved(r.selfSignup);
      setOk(true);
    } catch (err) {
      setError(errorText(err, "We couldn't save that setting."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} className="mb-6 flex flex-wrap items-end gap-3">
      <div>
        <label className="label" htmlFor="self-signup">Self-signup</label>
        <select id="self-signup" className="input w-full sm:w-104!" value={mode} onChange={(e) => { setMode(e.target.value as "request" | "off"); setOk(false); }}>
          <option value="request">On — requests wait for approval</option>
          <option value="off">Off — office creates accounts</option>
        </select>
        <p className="text-sm text-text-muted mt-1">People ask for an account on /join. Turning this off never removes existing accounts.</p>
      </div>
      <button type="submit" className="btn-secondary" disabled={busy || mode === saved}>{busy ? "Saving…" : "Save self-signup"}</button>
      {ok && <span className="text-sm text-text-muted">Saved</span>}
      {error && <span className="text-sm text-danger">{error}</span>}
    </form>
  );
}

export function AdminPage() {
  const { user, loading: authLoading, isAdmin } = useAuth();
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = (searchParams.get("tab") as AdminTab) || "approvals";
  const [pendingCount, setPendingCount] = useState(0);
  const [users, setUsers] = useState<User[]>([]);
  const [usersLoading, setUsersLoading] = useState(true);

  const loadUsers = useCallback(async () => {
    try {
      const { users } = await AdminUsers.list();
      setUsers(users);
    } catch {
      // Users tab shows its own empty state on failure.
    } finally {
      setUsersLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!authLoading && user && isAdmin) loadUsers();
    else if (!authLoading) setUsersLoading(false);
  }, [authLoading, user, isAdmin, loadUsers]);

  if (!authLoading && (!user || !isAdmin)) {
    return (
      <div className="max-w-4xl mx-auto px-4 py-12 text-center text-text-muted">
        This page is for church admins only.
      </div>
    );
  }

  const tabs: { id: AdminTab; label: string; badge?: number }[] = [
    { id: "approvals", label: "Approvals", badge: pendingCount },
    { id: "groups", label: "Groups" },
    { id: "users", label: "Users" },
    { id: "invite", label: "Invite" },
  ];

  return (
    <div className="max-w-5xl mx-auto px-4 py-8">
      <h1 className="text-3xl font-bold mb-6">Admin</h1>
      <ChurchTimezone />
      <SelfSignupSetting />
      <p className="mb-6">
        <Link to="/events/new" className="text-primary font-semibold">
          Schedule a church-wide event
        </Link>
      </p>
      <div className="flex gap-1 border-b border-border mb-6">
        {tabs.map((t) => (
          <button
            key={t.id}
            onClick={() => setSearchParams(t.id === "approvals" ? {} : { tab: t.id })}
            className={`px-4 py-2.5 text-sm font-semibold border-b-2 -mb-px ${
              tab === t.id
                ? "border-primary text-primary"
                : "border-transparent text-text-muted hover:text-text"
            }`}
          >
            {t.label}
            {t.badge ? (
              <span className="ml-1.5 inline-flex items-center justify-center min-w-5 h-5 px-1 rounded-full bg-primary text-primary-foreground text-xs">
                {t.badge}
              </span>
            ) : null}
          </button>
        ))}
      </div>
      {tab === "approvals" && <ApprovalsTab onCount={setPendingCount} />}
      {tab === "groups" && <GroupsTab users={users} />}
      {tab === "users" && (
        usersLoading ? <div className="text-text-muted">Loading…</div> : <UsersTab users={users} reload={loadUsers} />
      )}
      {tab === "invite" && <InviteTab />}
    </div>
  );
}

