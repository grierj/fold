import { Form } from "../components/ui/Form";
import { AddRequestForm } from "../components/AddRequestForm";
import { PeoplePicker } from "../components/ui/PeoplePicker";
import type { PersonSearchResult } from "../types";
import { ContactDetails } from "../components/ContactDetails";
import { GroupDepartureDialog } from "../components/GroupDepartureDialog";
import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react";
import { Badge } from "../components/ui/Badge";
import { Tabs } from "../components/ui/Tabs";
import { roleLabel, accountStatusLabel, groupStatusLabel } from "../lib/status";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Admin, AdminAccountRequests, AdminGuardianLinks, AdminGroups, AdminRooms, AdminUsers, ApiError, Groups, Invitations, RoomRequests, Site } from "../api";
import { RejectGroupDialog } from "../components/admin/RejectGroupDialog";
import { RoomsTab, sortRooms } from "../components/admin/RoomsTab";
import { RoomRequestsList } from "../components/admin/RoomRequestsList";
import { MatchLocations } from "../components/admin/MatchLocations";
import { ZonePicker } from "../components/ui/ZonePicker";
import { FormActions } from "../components/ui/FormActions";
import { AccountAgeFields } from "../components/AccountAgeFields";
import type { AgeBand, AccountRequest, DepartureConfirmation, GuardianLink, GuardianNomination, GroupSummary, Member, Room, RoomRequest, User } from "../types";
import { useAuth } from "../auth";
import { Menu } from "../components/ui/Menu";
import { Avatar } from "../components/Avatar";
import { DeleteUserPanel } from "../components/DeleteUserPanel";
import { GuardianLinkCard } from "../components/GuardianLinkCard";
import { Button, ButtonLink } from "../components/ui/Button";
import { errorCode, errorText } from "../lib/errors";
import { Checkbox, Choices, Field, FormError, Input, Radio, Select, showFieldErrors, Textarea } from "../components/ui/Field";
import { ErrorNotice } from "../components/ErrorNotice";
import { EmptyState } from "../components/ui/EmptyState";
import { Chip } from "../components/ui/Chip";
import { SearchBox } from "../components/ui/SearchBox";
import { Illustration } from "../components/ui/Illustration";
import { Skeleton } from "../components/ui/Skeleton";
import { ContactHistory } from "../contact-history-api";
import { useUnsavedChanges } from "../lib/unsavedChanges";

type AdminTab = "approvals" | "groups" | "rooms" | "users" | "invite" | "settings";

const TeenConsentPanel = lazy(() => import("../components/TeenConsentPanel"));

// ---------------------------------------------------------------------------
// Approvals
// ---------------------------------------------------------------------------

type Decision = { kind: "approved" | "rejected"; id: number; name: string; leaderName: string };

function PendingCard({ group, onDone }: { group: GroupSummary; onDone: (decision: Decision) => void }) {
  const [tags, setTags] = useState(group.tags.join(", "));
  // The leader's tags show as plain text; the field opens only on "Edit tags",
  // and only edited tags are sent (DEC-99).
  const [editingTags, setEditingTags] = useState(false);
  const [busy, setBusy] = useState(false);
  const [rejecting, setRejecting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const tagsInput = useRef<HTMLInputElement>(null);

  // "Edit tags" unmounts when the field opens, so focus moves to the field.
  useEffect(() => {
    if (editingTags) tagsInput.current?.focus();
  }, [editingTags]);

  async function approve() {
    setBusy(true);
    setError(null);
    try {
      await Admin.approve(
        group.id,
        editingTags
          ? tags
              .split(",")
              .map((t) => t.trim())
              .filter(Boolean)
          : undefined
      );
      onDone({ kind: "approved", id: group.id, name: group.name, leaderName: group.leaderName });
    } catch (e) {
      setError(errorText(e, "We couldn't approve that."));
    } finally {
      setBusy(false);
    }
  }

  async function reject(message: string) {
    await Admin.reject(group.id, message || undefined);
    setRejecting(false);
    onDone({ kind: "rejected", id: group.id, name: group.name, leaderName: group.leaderName });
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
      {editingTags ? (
        <div className="mt-4">
          <Field id={`admin-tags-${group.id}`} label="Tags" optional hint="Separate tags with commas, for example young-adults, bible-study.">
            <Input ref={tagsInput} value={tags} onChange={(e) => setTags(e.target.value)} />
          </Field>
        </div>
      ) : (
        <p className="mt-3 flex flex-wrap items-center gap-x-3 text-sm text-text-muted">
          <span>Tags: {group.tags.length > 0 ? group.tags.join(", ") : "none"}</span>
          <Button variant="link" aria-label={`Edit tags for ${group.name}`} onClick={() => setEditingTags(true)}>Edit tags</Button>
        </p>
      )}
      {error && <FormError layout="mt-2">{error}</FormError>}
      <div className="flex gap-3 mt-4">
        <Button variant="primary" onClick={approve} disabled={busy}>
          {busy ? "…" : "Approve"}
        </Button>
        <Button variant="danger-outline" onClick={() => setRejecting(true)} disabled={busy}>
          Reject
        </Button>
      </div>
      {rejecting && <RejectGroupDialog groupName={group.name} onReject={reject} onCancel={() => setRejecting(false)} />}
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
          {request.guardianEmail && <p className="text-sm text-text-muted mt-3 break-words">Nominated guardian: {request.guardianEmail}. Approval asks an admin to check the link if this adult has an active account. Otherwise, help the adult join, then ask them to claim the teen from their profile.</p>}
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

function ApprovalsTab({ onCount, onGroupDecided }: { onCount: (n: number) => void; onGroupDecided: () => void }) {
  const [groups, setGroups] = useState<GroupSummary[]>([]);
  const [deletions, setDeletions] = useState<GroupSummary[]>([]);
  const [requests, setRequests] = useState<AccountRequest[]>([]);
  const [links, setLinks] = useState<GuardianLink[]>([]);
  const [nominations, setNominations] = useState<GuardianNomination[]>([]);
  const [announce, setAnnounce] = useState("");
  // What the admin just decided on a new group; stays until the next action or
  // a change of tab (the tab unmounts), so the outcome is never a flash.
  const [decision, setDecision] = useState<Decision | null>(null);
  const decisionRef = useRef<HTMLParagraphElement>(null);
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
      setNominations(guardian.nominations);
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

  // The decided card is gone from the list, so focus moves to the outcome.
  useEffect(() => {
    if (decision) decisionRef.current?.focus();
  }, [decision]);

  // Any other decision clears the line: it described the one before.
  const loadAfterOther = useCallback(() => {
    setDecision(null);
    void load();
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
      {decision && (
        <p ref={decisionRef} role="status" tabIndex={-1} className="mb-4 rounded-lg border border-border bg-sunken px-4 py-3 text-sm focus:outline-none">
          “{decision.name}” {decision.kind}. {decision.leaderName ? `${decision.leaderName} was told.` : "Its leaders were told."}
          {decision.kind === "approved" && (
            <>{" "}<Link to={`/groups/${decision.id}`} className="text-link">View group</Link></>
          )}
        </p>
      )}
      {loading && <Skeleton kind="lines" />}
      {!loading && error && <ErrorNotice message={error} onRetry={load} />}
      {!loading && !error && groups.length === 0 && deletions.length === 0 && requests.length === 0 && links.length === 0 && nominations.length === 0 && (
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
            <AccountRequestCard key={r.id} request={r} onDone={loadAfterOther} />
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
                loadAfterOther();
              }}
            />
          ))}
        </div>
      )}
      {nominations.length > 0 && <section className="space-y-4 mb-8" aria-label="Guardians who need an account">
        <h2 className="text-xl font-bold">Guardians who need an account</h2>
        {nominations.map((n) => <div key={n.requestId} className="bg-surface rounded-lg border border-border p-6 text-sm">
          <p className="font-semibold">{n.teenName}</p>
          <p className="mt-2 break-words">Nominated guardian: {n.guardianEmail}</p>
          <p className="text-text-muted mt-2">Help this adult get an active account. Then ask them to open Profile and request a guardian link using the teen's email. You will still need to confirm that link.</p>
          <Link to="/admin?tab=users" className="inline-block text-primary font-semibold py-3">Manage accounts</Link>
        </div>)}
      </section>}
      {groups.length > 0 && (
        <div className="space-y-4">
          <h2 className="text-xl font-bold">New groups</h2>
          {groups.map((g) => (
            <PendingCard
              key={g.id}
              group={g}
              onDone={(d) => {
                setDecision(d);
                onGroupDecided();
                void load();
              }}
            />
          ))}
        </div>
      )}
      {deletions.length > 0 && (
        <div className="space-y-4 mt-8">
          <h2 className="text-xl font-bold">Deletion requests</h2>
          {deletions.map((g) => (
            <DeletionCard key={g.id} group={g} onDone={loadAfterOther} />
          ))}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Groups: list, edit (via group page), manage members
// ---------------------------------------------------------------------------

function MemberManager({ group, users, onUnavailable, fallbackFocus }: {
  group: GroupSummary;
  users: User[];
  onUnavailable: (groupId: number) => void;
  fallbackFocus: () => void;
}) {
  const [members, setMembers] = useState<Member[] | null>(null);
  const [membersLoading, setMembersLoading] = useState(true);
  const [addUserId, setAddUserId] = useState("");
  const [selection, setSelection] = useState<PersonSearchResult | null>(null);
  const [askPerson, setAskPerson] = useState<PersonSearchResult | null>(null);
  const [addRole, setAddRole] = useState<"member" | "leader">("member");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [departureMember, setDepartureMember] = useState<number | null>(null);
  const [departureStatus, setDepartureStatus] = useState<string | null>(null);
  const [accessUnavailable, setAccessUnavailable] = useState(false);

  const load = useCallback(async (collapseOnDenied = true) => {
    setMembers(null);
    setMembersLoading(true);
    setError(null);
    try {
      const { members: nextMembers } = await Groups.members(group.id);
      setMembers(nextMembers);
      setAccessUnavailable(false);
      return true;
    } catch (e) {
      const denied = e instanceof ApiError && (e.status === 401 || e.status === 403 || e.status === 404);
      setAccessUnavailable(denied);
      setError(errorText(e, "We couldn't load the members."));
      if (denied && collapseOnDenied) onUnavailable(group.id);
      return false;
    } finally {
      setMembersLoading(false);
    }
  }, [group.id, onUnavailable]);

  useEffect(() => {
    void load();
  }, [load]);

  async function add() {
    if (selection?.teen) { setAskPerson(selection); return; }
    if (!selection && !addUserId) return;
    const target = selection ? { selectionId: selection.selectionId } : Number(addUserId);
    setBusy(true);
    setError(null);
    try {
      try {
        await AdminGroups.addMember(group.id, target, addRole);
      } catch (err) {
        // Full group: going over the limit is an explicit, audited choice (DEC-39).
        if (errorCode(err) !== "group_full") throw err;
        if (!confirm(`“${group.name}” is full. Add this person anyway?`)) return;
        await AdminGroups.addMember(group.id, target, addRole, true);
      }
      setAddUserId("");
      setSelection(null);
      await load();
    } catch (e) {
      setError(errorCode(e) === "teen_add_approval_required" ? "Select this person by name, then choose Ask to add. An account ID cannot be used to send an ask." : errorText(e, "We couldn't add that person."));
    } finally {
      setBusy(false);
    }
  }

  function remove(m: Member) {
    setError(null);
    setDepartureStatus(null);
    setDepartureMember(m.id);
  }

  async function completedDeparture(result: DepartureConfirmation) {
    setDepartureMember(null);
    setDepartureStatus(`${result.keptCount} ${result.keptCount === 1 ? "date" : "dates"} kept · ${result.endedCount} ${result.endedCount === 1 ? "date" : "dates"} ended.`);
    await load();
  }

  async function departureUnavailable() {
    await load(false);
  }

  function cancelDeparture() {
    setDepartureMember(null);
    if (accessUnavailable) onUnavailable(group.id);
  }

  const memberIds = new Set((members ?? []).map((m) => m.id));
  const candidates = users.filter((u) => !memberIds.has(u.id) && u.accountType !== "teen");

  return (
    <div className="mt-3 border-t border-border pt-3">
      {error && <FormError layout="mb-2">{error}</FormError>}
      {departureStatus && <p role="status" className="mb-2 rounded-lg border border-border bg-sunken px-3 py-2 text-sm">{departureStatus}</p>}
      {membersLoading && <p role="status" className="py-2 text-sm text-text-muted">Loading members…</p>}
      {!accessUnavailable && !membersLoading && (
        <>
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
                  <span className="text-xs text-text-muted">· {roleLabel(m.role)}</span>
                  <ContactDetails email={m.email} phone={m.phone} />
                </div>
                <Button variant="danger-outline" onClick={() => remove(m)} disabled={busy || departureMember !== null}>
                  Remove
                </Button>
              </li>
            ))}
          </ul>
          {members && members.length === 0 && (
            <p className="text-sm text-text-muted py-2">No members yet.</p>
          )}
          <div className="mt-3 max-w-xl space-y-4">
            <Field label="Find a person by name"><PeoplePicker value={selection} disabled={busy} onChange={(person) => { setSelection(person); setAskPerson(null); setAddUserId(""); setError(null); }} /></Field>
            <Field label="Or choose an adult by email">
              <Select value={addUserId} onChange={(e) => { setAddUserId(e.target.value); setSelection(null); setAskPerson(null); }}>
                <option value="">Select…</option>
                {candidates.map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.displayName || u.name}{u.accountType === "teen" ? " (Teen)" : ""} ({u.email})
                  </option>
                ))}
              </Select>
            </Field>
            {!selection?.teen && <Choices legend="Role">
              <Radio name={`admin-role-${group.id}`} label="Member" checked={addRole === "member"} onChange={() => setAddRole("member")} />
              <Radio name={`admin-role-${group.id}`} label="Leader" checked={addRole === "leader"} onChange={() => setAddRole("leader")} />
            </Choices>}
            <Button variant="secondary" onClick={add} disabled={busy || (!selection && !addUserId)}>
              {busy ? "…" : selection?.teen ? "Ask to add" : "Add"}
            </Button>
            {askPerson && <AddRequestForm key={askPerson.selectionId} person={askPerson} scope={{ contextType: "group", contextId: group.id }} full={group.memberLimit !== null && group.memberCount >= group.memberLimit} onClose={() => setAskPerson(null)} />}
          </div>
        </>
      )}
      {departureMember !== null && (
        <GroupDepartureDialog
          groupId={group.id}
          subjectId={departureMember}
          onCancel={cancelDeparture}
          onUnavailable={departureUnavailable}
          onCompleted={completedDeparture}
          fallbackFocus={fallbackFocus}
        />
      )}
    </div>
  );
}

// Pending groups wait on the admin, so they lead; the rest follow by name (DEC-99).
function sortGroups(groups: GroupSummary[]): GroupSummary[] {
  const rank = (g: GroupSummary) => (g.status === "pending" ? 0 : 1);
  return [...groups].sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name));
}

// The list lives in AdminPage (it also feeds the tab count), so opening the tab
// shows what is already loaded and a delete updates the one source.
function GroupsTab({ users, groups, error, reload, onDeleted }: {
  users: User[];
  groups: GroupSummary[] | null;
  error: string | null;
  reload: () => void;
  onDeleted: (id: number) => void;
}) {
  const [query, setQuery] = useState("");
  const [openMembers, setOpenMembers] = useState<number | null>(null);
  const [deletingId, setDeletingId] = useState<number | null>(null);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [deleted, setDeleted] = useState("");
  const deletedRef = useRef<HTMLParagraphElement>(null);
  const manageMemberButtons = useRef(new Map<number, HTMLButtonElement>());
  const closeMembers = useCallback((groupId: number) => {
    setOpenMembers((current) => current === groupId ? null : current);
  }, []);

  // The deleted row is gone, so focus moves to the line that says so (the Menu
  // that opened it went with the row).
  useEffect(() => {
    if (deleted) deletedRef.current?.focus();
  }, [deleted]);

  async function deleteGroup(g: GroupSummary) {
    if (!confirm(`Delete “${g.name}”? This removes the group, its members, and its events.`)) return;
    setDeletingId(g.id);
    setDeleteError(null);
    setDeleted("");
    try {
      await Admin.deleteGroup(g.id);
      onDeleted(g.id);
      setDeleted(`“${g.name}” was deleted.`);
      setOpenMembers((v) => (v === g.id ? null : v));
    } catch (e) {
      setDeleteError(errorText(e, "We couldn't delete that."));
    } finally {
      setDeletingId(null);
    }
  }

  if (!groups) return error ? <ErrorNotice message={error} onRetry={reload} /> : <Skeleton kind="lines" />;

  const needle = query.trim().toLowerCase();
  const shown = sortGroups(groups).filter((g) => !needle || g.name.toLowerCase().includes(needle) || g.leaderName.toLowerCase().includes(needle));

  return (
    <div className="space-y-3">
      {groups.length > 0 && <SearchBox id="admin-group-search" label="Search groups" value={query} onChange={setQuery} />}
      {deleteError && <FormError>{deleteError}</FormError>}
      {/* Always rendered so screen readers announce the text when it arrives; empty, it takes no room. */}
      <p ref={deletedRef} role="status" tabIndex={-1} className={deleted ? "text-sm text-text-muted focus:outline-none" : "sr-only"}>{deleted}</p>
      {shown.map((g) => (
        <div key={g.id} className="bg-surface rounded-lg border border-border p-5">
          <div className="flex items-center gap-3 flex-wrap">
            <div className="grow min-w-0">
              <Link to={`/groups/${g.id}`} className="inline-flex min-h-11 items-center font-bold hover:text-primary">
                {g.name}
              </Link>
              <div className="text-xs text-text-muted mt-0.5">
                {/* Only what needs attention gets a label; "Active" is the usual case (DEC-99). */}
                {g.status !== "active" && (
                  <span
                    className={`inline-block rounded-full px-2 py-0.5 font-semibold ${
                      g.status === "pending" ? "bg-warning-subtle text-warning-on-subtle" : "bg-sunken text-text-muted"
                    }`}
                  >
                    {groupStatusLabel(g.status)}
                  </span>
                )}
                {g.visibility === "invite_only" && (
                  <span className={`${g.status !== "active" ? "ml-1.5 " : ""}inline-block rounded-full px-2 py-0.5 font-semibold bg-inverse text-inverse-foreground`}>
                    Invite only
                  </span>
                )}
                <p className="mt-1">
                  {g.memberCount} {g.memberCount === 1 ? "member" : "members"} · {g.leaderName ? `Organized by ${g.leaderName}` : "No leader assigned"}
                </p>
              </div>
            </div>
            <Button
              ref={(button) => {
                if (button) manageMemberButtons.current.set(g.id, button);
                else manageMemberButtons.current.delete(g.id);
              }}
              variant="secondary"
              onClick={() => setOpenMembers((v) => (v === g.id ? null : g.id))}
            >
              {openMembers === g.id ? "Hide members" : "Manage members"}
            </Button>
            {/* Manage members is the row's one action; Delete lives in More (DEC-63). */}
            <Menu
              label={`More for ${g.name}`}
              items={[{ label: "Delete", onSelect: () => void deleteGroup(g), danger: true, disabled: deletingId === g.id }]}
            />
          </div>
          {openMembers === g.id && <MemberManager group={g} users={users} onUnavailable={closeMembers}
            fallbackFocus={() => manageMemberButtons.current.get(g.id)?.focus()} />}
        </div>
      ))}
      {groups.length === 0 && (
        <EmptyState title="No groups yet" body="Groups people start, and groups you add, show up here." />
      )}
      {groups.length > 0 && shown.length === 0 && (
        <EmptyState
          title="No groups match"
          body="Try a different name, or clear the search."
          action={<Button variant="secondary" onClick={() => setQuery("")}>Clear search</Button>}
        />
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
    <Form noValidate onSubmit={save} className="bg-surface rounded-lg border border-border p-6 space-y-4 mt-4">
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
            <Field id="admin-password" label="Password" optional hint="At least 10 characters, and at most 72 bytes (accents and emoji take more). Leave blank to create an invited account that can't sign in until a password is set.">
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
    </Form>
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
    <Form noValidate onSubmit={reset} className="mt-2 max-w-xl space-y-4">
      {error && <FormError>{error}</FormError>}
      <Field id="admin-new-password-for" label={`New password for ${user.email}`} hint="Must be at least 10 characters, and at most 72 bytes (accents and emoji take more).">
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
    </Form>
  );
}

type UserFilter = "all" | "admin" | "disabled" | "invited";
const USER_FILTERS: { id: UserFilter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "admin", label: "Admins" },
  { id: "disabled", label: "Disabled" },
  { id: "invited", label: "Invited" },
];
function matchesUserFilter(u: User, filter: UserFilter): boolean {
  return filter === "all" || (filter === "admin" ? u.role === "admin" : u.status === filter);
}

function UsersTab({ users, reload }: { users: User[]; reload: () => void }) {
  const navigate = useNavigate();
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<UserFilter>("all");
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

  async function openContactHistory(u: User) {
    if (!u.email.trim()) { setError("Refresh the user list, then try opening this person's history again."); return; }
    setBusy(true);
    setError(null);
    setFeedStatus("");
    try {
      const { people } = await ContactHistory.people({ email: u.email });
      const person = people[0];
      if (!person || people.length !== 1) {
        setError("We couldn't find contact access history for that person.");
        return;
      }
      navigate(`/admin/users/${encodeURIComponent(person.id)}/contact-history`);
    } catch (e) {
      setError(errorText(e, "We couldn't open contact access history."));
    } finally {
      setBusy(false);
    }
  }

  async function copyPersonId(u: User) {
    if (!u.email.trim()) { setError("Refresh the user list, then try getting this person's ID again."); return; }
    setBusy(true);
    setError(null);
    setFeedStatus("");
    try {
      const { people } = await ContactHistory.people({ email: u.email });
      const person = people[0];
      if (!person || people.length !== 1) {
        setError("We couldn't find the person ID for that person.");
        return;
      }
      try {
        await navigator.clipboard?.writeText(person.id);
      } catch {
        // The visible status is also the fallback when clipboard access is unavailable.
      }
      setFeedStatus(`Person ID for ${person.name}: ${person.id}`);
    } catch (e) {
      setError(errorText(e, "We couldn't get that person ID."));
    } finally {
      setBusy(false);
    }
  }

  // Deleting or disabling opens a panel that also picks successors (DEC-36).
  const [leaving, setLeaving] = useState<{ id: number; action: "delete" | "disable" } | null>(null);

  const needle = query.trim().toLowerCase();
  const shown = users.filter((u) => matchesUserFilter(u, filter) && (!needle || [u.name, u.displayName, u.email].some((v) => v.toLowerCase().includes(needle))));

  return (
    <div>
      <div className="flex flex-wrap justify-end gap-2 mb-4">
        <ButtonLink variant="secondary" to="/admin/contact-history">Find contact access history</ButtonLink>
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
      {users.length > 0 && (
        <div className="mb-4 space-y-3">
          <SearchBox id="admin-user-search" label="Search by name or email" value={query} onChange={setQuery} />
          <div role="group" aria-label="Filter users" className="flex flex-wrap gap-2">
            {USER_FILTERS.map((f) => (
              <Chip key={f.id} selected={filter === f.id} onClick={() => setFilter(f.id)}>{f.label}</Chip>
            ))}
          </div>
        </div>
      )}
      {users.length > 0 && shown.length === 0 && (
        <EmptyState
          title="No one matches"
          body="Try a different name or email, or choose another filter."
          action={<Button variant="secondary" onClick={() => { setQuery(""); setFilter("all"); }}>Clear search and filter</Button>}
        />
      )}
      {/* Below md each user is a stacked card with Edit and More (PR #65
          review F1); from md up it's a table. */}
      {/* Not overflow-hidden: a row's More menu must be able to open past the card. */}
      {(users.length === 0 || shown.length > 0) && <div className="bg-surface rounded-lg border border-border overflow-visible">
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
            {shown.map((u) => (
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
                    {roleLabel(u.role)}
                  </span>
                  {u.status !== "active" && (
                    <span className="ml-1 inline-block rounded-full px-2 py-0.5 text-xs font-semibold bg-warning-subtle text-warning-on-subtle">
                      {accountStatusLabel(u.status)}
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
                      { label: "Contact access history", onSelect: () => void openContactHistory(u), disabled: busy },
                      { label: "Copy person ID", onSelect: () => void copyPersonId(u), disabled: busy },
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
      </div>}
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
  // Leaving with an unsaved zone asks first (DEC-98).
  const { guard } = useUnsavedChanges(saved !== "" && tz.trim() !== saved);

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
    <Form noValidate onSubmit={save} className="mb-6 max-w-xl space-y-4">
      {guard}
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
        {ok && <span role="status" className="text-sm text-success">Saved</span>}
      </FormActions>
      {error && <FormError>{error}</FormError>}
    </Form>
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
    <Form noValidate onSubmit={submit} className="bg-surface rounded-lg border border-border p-6 space-y-4 max-w-xl">
      {error && <FormError>{error}</FormError>}
      {status && <p role="status" className="text-success text-sm">{status}</p>}
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
          <option value="">Don't add to a group yet</option>
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
    </Form>
  );
}

// Self-signup mode (DEC-4, M1.3): request (default) or off. There is
// no open mode; every request waits for an admin either way. It is one on/off
// choice, so it saves when changed and goes back if saving fails (DEC-98).
function SelfSignupSetting() {
  const [mode, setMode] = useState<"request" | "off">("request");
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState(false);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let alive = true;
    Site.get()
      .then((s) => {
        if (!alive) return;
        setMode(s.selfSignup);
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, []);

  async function change(next: "request" | "off") {
    // The options are disabled while a save is in flight, so changes are one
    // at a time and a failure goes back to the value from before this save.
    const previous = mode;
    setSaving(true);
    setMode(next);
    setError(null);
    setOk(false);
    try {
      const r = await AdminAccountRequests.setSelfSignup(next);
      setMode(r.selfSignup);
      setOk(true);
    } catch (err) {
      setMode(previous);
      setError(errorText(err, "We couldn't save that setting."));
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="mb-6 max-w-xl space-y-2">
      <Choices legend="Self-signup" hint="People ask for an account on /join. Turning this off never removes existing accounts.">
        <Radio
          name="self-signup"
          label="On"
          description="Requests wait for an admin's approval."
          checked={mode === "request"}
          disabled={saving}
          onChange={() => void change("request")}
        />
        <Radio
          name="self-signup"
          label="Off"
          description="The church office creates accounts."
          checked={mode === "off"}
          disabled={saving}
          onChange={() => void change("off")}
        />
      </Choices>
      <p role="status" className="text-sm text-success">{ok ? "Saved" : ""}</p>
      {error && <FormError>{error}</FormError>}
    </div>
  );
}

// Rooms on/off (PLAN 9.1, M2.1a). One on/off choice, so like self-signup it
// saves when changed and goes back if saving fails (DEC-98). Turning it on
// shows the Rooms tab at once; turning it off hides rooms and deletes nothing.
// `enabled` is what AdminPage already read from /site (null until it answers).
function RoomsSetting({ enabled, onChanged }: { enabled: boolean | null; onChanged: (enabled: boolean) => void }) {
  // The choice shown while a save is in flight; null means show `enabled`.
  const [pending, setPending] = useState<boolean | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState(false);
  const [saving, setSaving] = useState(false);

  async function change(next: boolean) {
    setSaving(true);
    setPending(next);
    setError(null);
    setOk(false);
    try {
      const r = await AdminRooms.setEnabled(next);
      onChanged(r.roomsEnabled);
      setOk(true);
    } catch (err) {
      setError(errorText(err, "We couldn't save that setting."));
    } finally {
      setPending(null);
      setSaving(false);
    }
  }

  return (
    <div className="mb-6 max-w-xl space-y-2">
      <Checkbox
        label="Use rooms"
        description="Book rooms for events and see what's booked. Turning this off hides rooms; nothing is deleted."
        checked={pending ?? enabled === true}
        disabled={saving || enabled === null}
        onChange={(e) => void change(e.target.checked)}
      />
      <p role="status" className="text-sm text-success">{ok ? "Saved" : ""}</p>
      {error && <FormError>{error}</FormError>}
    </div>
  );
}

// Room bookings need an admin's approval (M2.1d, DEC-102). Like the Rooms
// switch it saves when changed and goes back if saving fails (DEC-98). It
// appears only while rooms are on; `enabled` is null until the rooms list answers.
function RoomApprovalSetting({ enabled, onChanged }: { enabled: boolean | null; onChanged: (enabled: boolean) => void }) {
  const [pending, setPending] = useState<boolean | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState(false);
  const [saving, setSaving] = useState(false);

  async function change(next: boolean) {
    setSaving(true);
    setPending(next);
    setError(null);
    setOk(false);
    try {
      const r = await AdminRooms.setApproval(next);
      onChanged(r.needApproval);
      setOk(true);
    } catch (err) {
      setError(errorText(err, "We couldn't save that setting."));
    } finally {
      setPending(null);
      setSaving(false);
    }
  }

  return (
    <div className="mb-6 max-w-xl space-y-2">
      <Checkbox
        label="Room bookings need an admin's approval"
        description="When this is on, leaders' and hosts' bookings wait for an admin. Admins' own bookings are confirmed at once. Turning it off leaves open requests here for you to decide."
        checked={pending ?? enabled === true}
        disabled={saving || enabled === null}
        onChange={(e) => void change(e.target.checked)}
      />
      <p role="status" className="text-sm text-success">{ok ? "Saved" : ""}</p>
      {error && <FormError>{error}</FormError>}
    </div>
  );
}

export function AdminPage() {
  const { user, loading: authLoading, isAdmin } = useAuth();
  const [searchParams, setSearchParams] = useSearchParams();
  const requestedTab = searchParams.get("tab");
  // Whether rooms are on: unknown (null) until /site answers. The Rooms tab
  // exists only while they are on; a bookmarked one waits for the answer.
  const [roomsEnabled, setRoomsEnabled] = useState<boolean | null>(null);
  // /site failing leaves roomsEnabled unknown; a bookmarked Rooms tab then says so and offers a retry.
  const [siteError, setSiteError] = useState<string | null>(null);
  const roomsShown = roomsEnabled === true || (roomsEnabled === null && requestedTab === "rooms");
  const tab: AdminTab = requestedTab && ["approvals", "groups", "users", "invite", "settings", ...(roomsShown ? ["rooms"] : [])].includes(requestedTab)
    ? requestedTab as AdminTab : "approvals";
  const [pendingCount, setPendingCount] = useState(0);
  const [users, setUsers] = useState<User[]>([]);
  const [usersLoading, setUsersLoading] = useState(true);
  // Counts for the Users and Groups chips; unknown (null) until loaded, so a
  // failed load shows no number rather than a wrong one.
  const [userCount, setUserCount] = useState<number | null>(null);
  // The groups list is loaded once here and shared: the Groups chip counts it
  // and the Groups tab shows it, so a delete or a decision updates one place.
  const [groups, setGroups] = useState<GroupSummary[] | null>(null);
  const [groupsError, setGroupsError] = useState<string | null>(null);
  const groupCount = groups ? groups.length : null;
  // Loaded once here like the groups: the Rooms chip counts the list the tab shows.
  const [rooms, setRooms] = useState<Room[] | null>(null);
  const [roomsError, setRoomsError] = useState<string | null>(null);
  const roomCount = rooms ? rooms.length : null;
  // Whether bookings wait for an admin, and the open requests (M2.1d). Both load with the rooms.
  const [needApproval, setNeedApproval] = useState<boolean | null>(null);
  const [requests, setRequests] = useState<RoomRequest[] | null>(null);
  const [requestsError, setRequestsError] = useState<string | null>(null);

  const loadUsers = useCallback(async () => {
    try {
      const { users } = await AdminUsers.list();
      setUsers(users);
      setUserCount(users.length);
    } catch {
      // Users tab shows its own empty state on failure.
    } finally {
      setUsersLoading(false);
    }
  }, []);

  // A failed load keeps any list already shown and leaves the count unknown
  // otherwise; the Groups tab offers a retry.
  const loadGroups = useCallback(async () => {
    setGroupsError(null);
    try {
      const { groups } = await AdminGroups.list();
      setGroups(groups);
    } catch (e) {
      setGroupsError(errorText(e, "We couldn't load the groups."));
    }
  }, []);
  const loadRooms = useCallback(async () => {
    setRoomsError(null);
    try {
      const { rooms, needApproval } = await AdminRooms.list();
      setRooms(rooms);
      setNeedApproval(needApproval === true);
    } catch (e) {
      setRoomsError(errorText(e, "We couldn't load the rooms."));
    }
  }, []);
  const loadRequests = useCallback(async () => {
    setRequestsError(null);
    try {
      const { requests } = await RoomRequests.list();
      setRequests(requests);
    } catch (e) {
      setRequestsError(errorText(e, "We couldn't load the bookings waiting for approval."));
    }
  }, []);
  const requestRemoved = useCallback((id: number) => setRequests((prev) => prev && prev.filter((r) => r.id !== id)), []);
  const loadSite = useCallback(() => {
    setSiteError(null);
    Site.get().then((s) => setRoomsEnabled(!!s.roomsEnabled), (e) => setSiteError(errorText(e, "We couldn't load the rooms.")));
  }, []);
  const roomSaved = useCallback((room: Room) => setRooms((prev) => sortRooms([...(prev ?? []).filter((r) => r.id !== room.id), room])), []);
  const groupDeleted = useCallback((id: number) => setGroups((prev) => prev && prev.filter((g) => g.id !== id)), []);

  useEffect(() => {
    if (!authLoading && user && isAdmin) {
      loadUsers();
      void loadGroups();
      loadSite();
    } else if (!authLoading) setUsersLoading(false);
  }, [authLoading, user, isAdmin, loadUsers, loadGroups, loadSite]);

  useEffect(() => {
    if (roomsEnabled) {
      void loadRooms();
      void loadRequests();
    }
  }, [roomsEnabled, loadRooms, loadRequests]);

  if (!authLoading && (!user || !isAdmin)) {
    return (
      <div className="max-w-4xl mx-auto px-4 py-12 text-center text-text-muted">
        <h1 className="text-3xl font-bold mb-6">Admin</h1>
        This page is for church admins only.
      </div>
    );
  }

  const tabs: { id: AdminTab; label: string; badge?: number; count?: number | null }[] = [
    { id: "approvals", label: "Approvals", badge: pendingCount },
    { id: "groups", label: "Groups", count: groupCount },
    ...(roomsShown ? [{ id: "rooms" as const, label: "Rooms", count: roomCount, badge: requests?.length }] : []),
    { id: "users", label: "Users", count: userCount },
    { id: "invite", label: "Invite" },
    { id: "settings", label: "Settings" },
  ];

  return (
    <div className="max-w-5xl mx-auto px-4 py-8">
      <h1 className="text-3xl font-bold mb-6">Admin</h1>
      <Tabs label="Admin sections" variant="chips" value={tab}
        onChange={(value) => setSearchParams(value === "approvals" ? {} : { tab: value })}
        options={tabs.map((t) => ({
          id: t.id,
          accessibleName: t.badge ? `${t.label}, ${t.badge} waiting` : t.count != null ? `${t.label}, ${t.count}` : t.label,
          label: <>{t.label}{t.badge ? (
            <span aria-hidden="true" className="ml-1.5 inline-flex items-center justify-center min-w-5 h-5 px-1 rounded-full bg-primary text-primary-foreground text-xs">
              {" "}{t.badge}
            </span>
          ) : null}{t.count != null && !t.badge && <span className="font-normal">{" "}{t.count}</span>}</>,
        }))}>

      {tab === "settings" && <section aria-labelledby="admin-settings-heading">
        <h2 id="admin-settings-heading" className="text-xl font-bold mb-4">Church settings</h2>
        <ChurchTimezone />
        <SelfSignupSetting />
        <RoomsSetting enabled={roomsEnabled} onChanged={setRoomsEnabled} />
        {roomsEnabled === true && <RoomApprovalSetting enabled={needApproval} onChanged={setNeedApproval} />}
        <ButtonLink to="/events/new" variant="secondary">Schedule a church-wide event</ButtonLink>
      </section>}
      {tab === "approvals" && <ApprovalsTab onCount={setPendingCount} onGroupDecided={loadGroups} />}
      {tab === "groups" && <GroupsTab users={users} groups={groups} error={groupsError} reload={loadGroups} onDeleted={groupDeleted} />}
      {tab === "rooms" && (roomsEnabled === true ? (
        <div className="space-y-8">
          <RoomRequestsList requests={requests} needApproval={needApproval} error={requestsError}
            reload={loadRequests} onRemoved={requestRemoved}>
            <RoomsTab rooms={rooms} error={roomsError} reload={loadRooms} onSaved={roomSaved} />
          </RoomRequestsList>
          <MatchLocations rooms={rooms} />
        </div>
      ) : (
        <RoomsTab rooms={rooms} error={roomsEnabled === null ? siteError : roomsError}
          reload={roomsEnabled === null ? loadSite : loadRooms} onSaved={roomSaved} />
      ))}
      {tab === "users" && (
        usersLoading ? <Skeleton kind="lines" /> : <UsersTab users={users} reload={loadUsers} />
      )}
      {tab === "invite" && <InviteTab />}
      </Tabs>
    </div>
  );
}

