import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react";
import { Badge } from "../components/ui/Badge";
import { Link, useSearchParams } from "react-router-dom";
import { Admin, AdminAccountRequests, AdminGuardianLinks, AdminGroups, AdminUsers, ApiError, Groups, Invitations, Site } from "../api";
import { ZonePicker } from "../components/ui/ZonePicker";
import { FormActions } from "../components/ui/FormActions";
import { AccountAgeFields } from "../components/AccountAgeFields";
import type { AgeBand, AccountRequest, GuardianLink, GroupSummary, Member, User } from "../types";
import { useAuth } from "../auth";
import { Menu } from "../components/ui/Menu";
import { Avatar } from "../components/Avatar";
import { DeleteUserPanel } from "../components/DeleteUserPanel";
import { GuardianLinkCard } from "../components/GuardianLinkCard";
import { Button } from "../components/ui/Button";
import { errorText } from "../lib/errors";
import { Checkbox, Choices, Field, FormError, Input, Radio, Select, showFieldErrors, Textarea } from "../components/ui/Field";
import { ErrorNotice } from "../components/ErrorNotice";
import { EmptyState } from "../components/ui/EmptyState";
import { Illustration } from "../components/ui/Illustration";
import { Skeleton } from "../components/ui/Skeleton";

type AdminTab = "approvals" | "groups" | "users" | "invite";

const TeenConsentPanel = lazy(() => import("../components/TeenConsentPanel"));

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
        <Field id="admin-tags" label="Tags" optional hint="Separate tags with commas, for example young-adults, bible-study.">
          <Input value={tags} onChange={(e) => setTags(e.target.value)} />
        </Field>
      </div>
      {error && <FormError layout="mt-2">{error}</FormError>}
      <div className="flex gap-3 mt-4">
        <Button variant="primary" onClick={approve} disabled={busy}>
          {busy ? "…" : "Approve"}
        </Button>
        <Button variant="danger-outline" onClick={reject} disabled={busy}>
          Reject
        </Button>
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
      {error && <FormError layout="mt-2">{error}</FormError>}
      <div className="flex gap-3 mt-4">
        <Button variant="danger-outline" onClick={approve} disabled={busy}>
          {busy ? "…" : "Delete group"}
        </Button>
        <Button variant="primary" onClick={keep} disabled={busy}>
          Keep group
        </Button>
      </div>
    </div>
  );
}

function AccountRequestCard({ request, onDone }: { request: AccountRequest; onDone: () => void }) {
  const [note, setNote] = useState("");
  const [grantConsent, setGrantConsent] = useState(false);
  const [evidence, setEvidence] = useState("");
  const [evidenceError, setEvidenceError] = useState<string | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function decide(approve: boolean) {
    if (approve && grantConsent && !evidence.trim()) {
      showFieldErrors(ref.current, () => setEvidenceError("Write how you received consent first."));
      return;
    }
    if (!approve && !confirm(`Reject the account request from ${request.name}? They'll be emailed.`)) return;
    setBusy(true);
    setError(null);
    try {
      if (approve) {
        await AdminAccountRequests.approve(request.id, note || undefined, undefined, grantConsent ? evidence.trim() : undefined);
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
  if (request.accountType === "teen") warnings.push("Teen: account consent required");
  if (request.warnings.duplicate) warnings.push("Duplicate pending request");
  if (request.warnings.honeypot) warnings.push("Honeypot filled");
  if (request.warnings.rateLimited) warnings.push("Many requests from one connection");

  return (
    <div ref={ref} className="bg-surface rounded-lg border border-border p-6">
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
              : request.source === "household_conversion"
                ? `Household account, invited by ${request.requestedByName || "a member"}`
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
        <Field id={`note-${request.id}`} label="Note" optional hint="Emailed to them if you reject the request.">
          <Input value={note} onChange={(e) => setNote(e.target.value)} maxLength={500} />
        </Field>
      </div>
      {request.accountType === "teen" && <div className="mt-4 space-y-3">
        <Checkbox label="Grant account consent with approval" description="Only choose this if you have a parent or guardian's consent on file." checked={grantConsent} onChange={(event) => setGrantConsent(event.target.checked)} />
        {grantConsent && <Field label="Consent evidence" hint="Describe how you received consent. Maximum 1,000 characters. This stays in the admin record." error={evidenceError ?? undefined}>
          <Textarea value={evidence} onChange={(event) => { setEvidence(event.target.value); setEvidenceError(null); }} maxLength={1000} rows={2} />
        </Field>}
      </div>}
      {error && <FormError layout="mt-2">{error}</FormError>}
      <div className="flex gap-3 mt-4">
        <Button variant="primary" onClick={() => decide(true)} disabled={busy}>{busy ? "…" : "Approve"}</Button>
        <Button variant="danger-outline" onClick={() => decide(false)} disabled={busy}>Reject</Button>
      </div>
    </div>
  );
}

function ApprovalsTab({ onCount }: { onCount: (n: number) => void }) {
  const [groups, setGroups] = useState<GroupSummary[]>([]);
  const [deletions, setDeletions] = useState<GroupSummary[]>([]);
  const [requests, setRequests] = useState<AccountRequest[]>([]);
  const [links, setLinks] = useState<GuardianLink[]>([]);
  const [announce, setAnnounce] = useState("");
  const focusLinks = useRef(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [pending, requested, accounts, guardian] = await Promise.all([
        Admin.pending(), Admin.deletions(), AdminAccountRequests.list(), AdminGuardianLinks.list(),
      ]);
      setLinks(guardian.guardianLinks);
      setGroups(pending.groups);
      setDeletions(requested.groups);
      setRequests(accounts.requests);
      onCount(pending.groups.length + requested.groups.length + accounts.requests.length + guardian.guardianLinks.length);
    } catch (e) {
      setError(errorText(e, "We couldn't load the approval queue."));
    } finally {
      setLoading(false);
    }
  }, [onCount]);

  useEffect(() => {
    load();
  }, [load]);

  // After a confirm or reject the card goes away: focus the next link card,
  // or the section heading, or the tab content when nothing is left (M1).
  const rootRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (loading || !focusLinks.current) return;
    focusLinks.current = false;
    const root = rootRef.current;
    const next = root?.querySelector<HTMLElement>("[data-link-card] textarea");
    (next ?? root?.querySelector<HTMLElement>("#household-links-heading") ?? root)?.focus();
  }, [links, loading]);

  return (
    <div ref={rootRef} tabIndex={-1} className="focus:outline-none">
      <p role="status" aria-live="polite" className="sr-only">{announce}</p>
      {loading && <Skeleton kind="lines" />}
      {!loading && error && <ErrorNotice message={error} onRetry={load} />}
      {!loading && !error && groups.length === 0 && deletions.length === 0 && requests.length === 0 && links.length === 0 && (
        <EmptyState
          illustration={<Illustration name="all-caught-up" />}
          title="All caught up"
          body="New group, account and household link requests show up here for you to review."
        />
      )}
      {requests.length > 0 && (
        <div className="space-y-4 mb-8">
          <h2 className="text-xl font-bold">Account requests</h2>
          {requests.map((r) => (
            <AccountRequestCard key={r.id} request={r} onDone={load} />
          ))}
        </div>
      )}
      {links.length > 0 && (
        <div className="space-y-4 mb-8">
          <h2 id="household-links-heading" tabIndex={-1} className="text-xl font-bold focus:outline-none">Household links</h2>
          {links.map((l) => (
            <GuardianLinkCard
              key={l.id}
              link={l}
              onDone={(msg) => {
                setAnnounce(msg);
                focusLinks.current = true;
                load();
              }}
            />
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
      {error && <FormError layout="mb-2">{error}</FormError>}
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
              {m.accountType === "teen" && <Badge>Teen</Badge>}
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
      <div className="mt-3 max-w-xl space-y-4">
        <Field id="admin-add-person" label="Add person">
          <Select value={addUserId} onChange={(e) => setAddUserId(e.target.value)}>
            <option value="">Select…</option>
            {candidates.map((u) => (
              <option key={u.id} value={u.id}>
                {u.displayName || u.name}{u.accountType === "teen" ? " (Teen)" : ""} ({u.email})
              </option>
            ))}
          </Select>
        </Field>
        <Choices legend="Role">
          <Radio name={`admin-role-${group.id}`} label="Member" checked={addRole === "member"} onChange={() => setAddRole("member")} />
          <Radio name={`admin-role-${group.id}`} label="Leader" checked={addRole === "leader"} onChange={() => setAddRole("leader")} />
        </Choices>
        <Button variant="secondary" onClick={add} disabled={busy || !addUserId}>
          {busy ? "…" : "Add"}
        </Button>
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
  const [retry, setRetry] = useState(0);

  useEffect(() => {
    (async () => {
      setLoading(true);
      setError(null);
      try {
        const { groups } = await AdminGroups.list();
        setGroups(groups);
      } catch (e) {
        setError(errorText(e, "We couldn't load the groups."));
      } finally {
        setLoading(false);
      }
    })();
  }, [retry]);

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

  if (loading) return <Skeleton kind="lines" />;
  if (error) return <ErrorNotice message={error} onRetry={() => setRetry((n) => n + 1)} />;

  return (
    <div className="space-y-3">
      {deleteError && <FormError>{deleteError}</FormError>}
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
            <Button variant="secondary" onClick={() => setOpenMembers((v) => (v === g.id ? null : g.id))}>
              {openMembers === g.id ? "Hide members" : "Manage members"}
            </Button>
            <Link to={`/groups/${g.id}`} className="btn-secondary py-1.5! px-3! text-sm">
              Open & edit
            </Link>
            <Button variant="danger-outline" onClick={() => deleteGroup(g)} disabled={deletingId === g.id}>
              {deletingId === g.id ? "…" : "Delete"}
            </Button>
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
  const [ageBand, setAgeBand] = useState<AgeBand>("adult");
  const [birthDate, setBirthDate] = useState("");
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) =>
    setForm((f) => ({ ...f, [k]: e.target.value }));

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      if (creating) {
        if (ageBand === "under13") return;
        await AdminUsers.create({ ...form, ageBand, birthDate: ageBand === "teen" ? birthDate : undefined, password: form.password || undefined });
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
      {error && <FormError>{error}</FormError>}
      <div className="grid sm:grid-cols-2 gap-4">
        <Field id="admin-email" label="Email (signs in with it)">
          <Input
            type="email"
            value={form.email}
            onChange={set("email")}
            maxLength={254}
            required
          />
        </Field>
        <Choices legend="Role">
          <Radio name="admin-user-role" label="Member" value="member" checked={form.role === "member"} onChange={set("role")} />
          <Radio name="admin-user-role" label="Admin" value="admin" checked={form.role === "admin"} onChange={set("role")} disabled={initial?.accountType === "teen" || (creating && ageBand === "teen")} />
        </Choices>
        {creating && (
          <div className="sm:col-span-2">
            <Field id="admin-password" label="Password" optional hint="At least 10 characters. Leave blank to create an invited account that can't sign in until a password is set.">
              <Input
                type="password"
                value={form.password}
                onChange={set("password")}
                autoComplete="new-password"
              />
            </Field>
          </div>
        )}
        <Field id="admin-display-name" label="Display name" optional>
          <Input value={form.displayName} onChange={set("displayName")} maxLength={100} />
        </Field>
        <Field id="admin-full-name" label="Full name">
          <Input value={form.name} onChange={set("name")} maxLength={100} required />
        </Field>
        <Field id="admin-phone" label="Phone" optional>
          <Input type="tel" value={form.phone} onChange={set("phone")} maxLength={30} />
        </Field>
      </div>
      {creating && <AccountAgeFields id="admin-user" label="Their age" ageBand={ageBand} birthDate={birthDate} onAgeBand={(band) => { setAgeBand(band); if (band === "teen") setForm((value) => ({ ...value, role: "member" })); }} onBirthDate={setBirthDate} />}
      <FormActions>
        <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={busy || (creating && ageBand === "under13")}>
          {busy ? "Saving…" : creating ? "Create user" : "Save user"}
        </Button>
        <Button variant="secondary" layout="w-full sm:w-auto" onClick={onCancel}>
          Cancel
        </Button>
      </FormActions>
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
        <Button variant="link" onClick={onDone}>
          Done
        </Button>
      </div>
    );
  }

  return (
    <form onSubmit={reset} className="mt-2 max-w-xl space-y-4">
      {error && <FormError>{error}</FormError>}
      <Field id="admin-new-password-for" label={`New password for ${user.email}`} hint="Must be at least 10 characters.">
        <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" required />
      </Field>
      <FormActions>
        <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={busy}>
          {busy ? "Saving…" : "Set password"}
        </Button>
        <Button variant="secondary" layout="w-full sm:w-auto" onClick={onDone}>
          Cancel
        </Button>
      </FormActions>
    </form>
  );
}

function UsersTab({ users, reload }: { users: User[]; reload: () => void }) {
  // Your own row has no Disable or Delete: the server refuses both
  // ("you cannot disable/delete your own account"), so don't offer them.
  const { user: me } = useAuth();
  const [creating, setCreating] = useState(false);
  const [editingId, setEditingId] = useState<number | null>(null);
  const [resetId, setResetId] = useState<number | null>(null);
  const [consentId, setConsentId] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [feedStatus, setFeedStatus] = useState("");
  const feedStatusRef = useRef<HTMLParagraphElement>(null);
  // The list is long at 390; bring the result into view (M17-REVIEW M3).
  useEffect(() => {
    if (feedStatus) feedStatusRef.current?.scrollIntoView?.({ block: "nearest" });
  }, [feedStatus]);

  async function revokeCalendarLink(u: User) {
    if (!confirm(`Revoke ${u.displayName || u.name}'s calendar link? It will stop working right away.`)) return;
    setBusy(true);
    setError(null);
    setFeedStatus("");
    try {
      const who = u.displayName || u.name;
      const r = await AdminUsers.revokeCalendarFeed(u.id);
      setFeedStatus(r.hadLink ? `Calendar link revoked for ${who}.` : `${who} didn't have a calendar link.`);
    } catch (e) {
      setError(errorText(e, "We couldn't revoke that link."));
    } finally {
      setBusy(false);
    }
  }

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
        <Button variant="primary" onClick={() => { setCreating(true); setEditingId(null); }}>
          New user
        </Button>
      </div>
      {error && <FormError layout="mb-3">{error}</FormError>}
      <p ref={feedStatusRef} role="status" className="text-success text-sm mb-3 empty:hidden">{feedStatus}</p>
      {creating && (
        <UserForm
          creating
          onSaved={() => { setCreating(false); reload(); }}
          onCancel={() => setCreating(false)}
        />
      )}
      {/* Below md each user is a stacked card with Edit and More (PR #65
          review F1); from md up it's a table. */}
      {/* Not overflow-hidden: a row's More menu must be able to open past the card. */}
      <div className="bg-surface rounded-lg border border-border overflow-visible">
        <table className="block md:table w-full text-sm">
          <thead className="hidden md:table-header-group">
            <tr className="text-left text-text-muted border-b border-border">
              <th className="px-4 py-3 font-medium">User</th>
              <th className="px-4 py-3 font-medium hidden lg:table-cell">Contact</th>
              <th className="px-4 py-3 font-medium">Role</th>
              <th className="px-4 py-3 font-medium text-right">Actions</th>
            </tr>
          </thead>
          <tbody className="block md:table-row-group divide-y divide-border">
            {users.map((u) => (
              <tr key={u.id} className="block md:table-row">
                <td className="block md:table-cell px-4 pt-3 md:py-3">
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
                  {consentId === u.id && <Suspense fallback={<p role="status">Loading consent…</p>}><TeenConsentPanel user={u} onUpdated={reload} onClose={() => setConsentId(null)} /></Suspense>}
                  {leaving?.id === u.id && (
                    <DeleteUserPanel
                      target={u}
                      action={leaving.action}
                      onDone={(done) => { setLeaving(null); if (done) reload(); }}
                    />
                  )}
                </td>
                <td className="px-4 py-3 text-text-muted hidden lg:table-cell">
                  {u.email || "—"}
                  {u.phone ? <div className="text-xs">{u.phone}</div> : null}
                </td>
                <td className="block md:table-cell px-4 pt-2 md:py-3">
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
                <td className="flex flex-wrap gap-2 px-4 pt-2 pb-3 md:table-cell md:py-3 md:text-right md:whitespace-nowrap">
                  <Button variant="ghost" size="sm" onClick={() => { setEditingId(u.id); setResetId(null); setCreating(false); }}>
                    Edit
                  </Button>
                  {/* Edit is the row's one action; the rest live in More (DEC-63).
                      Your own row has no Disable or Delete: the server refuses both. */}
                  <Menu
                    tone="quiet"
                    label={`More for ${u.displayName || u.name}`}
                    items={[
                      ...(u.accountType === "teen" || u.hasTeenConsentHistory ? [{ label: u.accountType === "teen" ? "Manage consent" : "Consent history", onSelect: () => { setConsentId(u.id); setEditingId(null); setResetId(null); setCreating(false); } }] : []),
                      { label: "Reset password", onSelect: () => { setResetId(u.id); setEditingId(null); setCreating(false); } },
                      { label: "Revoke calendar link", onSelect: () => void revokeCalendarLink(u), danger: true, disabled: busy },
                      ...(u.id === me?.id ? [] : [
                        {
                          label: u.status === "disabled" ? "Enable" : "Disable",
                          onSelect: () => (u.status === "disabled" ? void setStatus(u, "active") : setLeaving({ id: u.id, action: "disable" })),
                          disabled: busy,
                        },
                        {
                          label: "Delete",
                          onSelect: () => { setLeaving({ id: u.id, action: "delete" }); setEditingId(null); setResetId(null); },
                          danger: true,
                          disabled: busy,
                        },
                      ]),
                    ]}
                  />
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
    <form onSubmit={save} className="mb-6 max-w-xl space-y-4">
      <Field id="church-tz" label="Church time zone" hint="New events start in this time zone; leaders can pick another when they schedule.">
        <ZonePicker
          forChurchSetting
          value={tz}
          onChange={(v) => {
            setTz(v);
            setOk(false);
          }}
        />
      </Field>
      <FormActions>
        <Button variant="secondary" layout="w-full sm:w-auto" type="submit" disabled={busy || tz.trim() === saved}>
          {busy ? "Saving…" : "Save time zone"}
        </Button>
        {ok && <span className="text-sm text-text-muted">Saved</span>}
      </FormActions>
      {error && <FormError>{error}</FormError>}
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
      {error && <FormError>{error}</FormError>}
      {status && <div className="text-success text-sm">{status}</div>}
      <p className="text-sm text-text-muted">
        They'll get an email link, set a password, and join with an active account — no
        approval step.
      </p>
      <Field id="inv-email" label="Email">
        <Input
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          required
        />
      </Field>
      <Field id="inv-name" label="Name" optional>
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          maxLength={100}
        />
      </Field>
      <Field id="inv-group" label="Group" optional>
        <Select
          value={groupId}
          onChange={(e) => setGroupId(e.target.value)}
        >
          <option value="">No group — they join Fold only</option>
          {groups.map((g) => (
            <option key={g.id} value={g.id}>
              {g.name}
            </option>
          ))}
        </Select>
      </Field>
      <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={busy}>
        {busy ? "Sending…" : "Send invitation"}
      </Button>
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
    <form onSubmit={save} className="mb-6 max-w-xl space-y-4">
      <Choices legend="Self-signup" hint="People ask for an account on /join. Turning this off never removes existing accounts.">
        <Radio
          name="self-signup"
          label="On"
          description="Requests wait for an admin's approval."
          checked={mode === "request"}
          onChange={() => { setMode("request"); setOk(false); }}
        />
        <Radio
          name="self-signup"
          label="Off"
          description="The church office creates accounts."
          checked={mode === "off"}
          onChange={() => { setMode("off"); setOk(false); }}
        />
      </Choices>
      <FormActions>
        <Button variant="secondary" layout="w-full sm:w-auto" type="submit" disabled={busy || mode === saved}>{busy ? "Saving…" : "Save self-signup"}</Button>
        {ok && <span className="text-sm text-text-muted">Saved</span>}
      </FormActions>
      {error && <FormError>{error}</FormError>}
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
        <Link to="/events/new" className="inline-flex items-center min-h-11 text-primary font-semibold">
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
        usersLoading ? <Skeleton kind="lines" /> : <UsersTab users={users} reload={loadUsers} />
      )}
      {tab === "invite" && <InviteTab />}
    </div>
  );
}

