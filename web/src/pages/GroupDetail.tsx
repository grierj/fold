import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ApiError, GroupCover, Groups, Meetings } from "../api";
import type { GroupDetailResponse, MeetingInstance, Visibility } from "../types";
import { useAuth } from "../auth";
import { DateBadge } from "../components/DateBadge";
import { RsvpButtons } from "../components/RsvpButtons";
import { Avatar } from "../components/Avatar";
import { ImageUpload } from "../components/ImageUpload";
import { coverImage, formatNextMeeting } from "../components/GroupCard";
import { focalPosition, prepareUpload } from "../lib/image";

type Tab = "events" | "about" | "members";

function rsvpTotal(r: Record<string, number>, key: string): number {
  return r[key] ?? 0;
}

function EventRow({
  instance,
  groupName,
  canManage,
  loggedIn,
  onRsvp,
  onCancelled,
}: {
  instance: MeetingInstance;
  groupName: string;
  canManage: boolean;
  loggedIn: boolean;
  onRsvp: (meetingId: number, date: string, status: "going" | "maybe" | "declined") => void;
  onCancelled: (meetingId: number, date: string) => void;
}) {
  const [cancelling, setCancelling] = useState(false);
  const time = new Date(instance.startsAt).toLocaleTimeString(undefined, {
    hour: "numeric",
    minute: "2-digit",
  });
  const loc = instance.location || "";

  async function cancel() {
    if (!confirm(`Cancel this instance on ${instance.date}?`)) return;
    setCancelling(true);
    try {
      await Meetings.cancelInstance(instance.meetingId, instance.date);
      onCancelled(instance.meetingId, instance.date);
    } catch (e) {
      alert(e instanceof Error ? e.message : "Cancel failed");
    } finally {
      setCancelling(false);
    }
  }

  return (
    <div className="bg-white rounded-xl border border-gray-200 p-4 flex gap-4">
      <DateBadge iso={instance.startsAt} />
      <div className="grow min-w-0">
        <div className="font-bold text-lg">{groupName}</div>
        <div className="text-sm text-gray-600">
          {time}
          {loc ? ` · ${loc}` : ""}
        </div>
        <div className="text-sm text-gray-500 mt-1">
          {rsvpTotal(instance.rsvps, "going")} going · {rsvpTotal(instance.rsvps, "maybe")} maybe ·{" "}
          {rsvpTotal(instance.rsvps, "declined")} can't go
        </div>
        {loggedIn && (
          <div className="mt-2">
            <RsvpButtons
              instance={instance}
              onChange={(status) => onRsvp(instance.meetingId, instance.date, status)}
            />
          </div>
        )}
      </div>
      {canManage && (
        <div className="shrink-0">
          <button
            onClick={cancel}
            disabled={cancelling}
            className="text-sm text-red-600 hover:text-red-800 font-medium"
          >
            {cancelling ? "Cancelling…" : "Cancel"}
          </button>
        </div>
      )}
    </div>
  );
}

export interface GroupFormData {
  name: string;
  description: string;
  location: string;
  tags: string[];
  imageUrl: string;
  coverFile?: File | null;
  coverFocal?: { x: number; y: number };
  visibility: Visibility;
  memberLimit: number | null;
}

function GroupForm({
  initial,
  saveLabel,
  onSaved,
  onCancel,
}: {
  initial: {
    name: string; description: string; location: string; tags: string[];
    imageUrl: string; coverFocalX: number; coverFocalY: number;
    visibility?: Visibility;
    memberLimit?: number | null;
  };
  saveLabel: string;
  onSaved: (data: GroupFormData) => Promise<void>;
  onCancel: () => void;
}) {
  const [name, setName] = useState(initial.name);
  const [description, setDescription] = useState(initial.description);
  const [location, setLocation] = useState(initial.location);
  const [tags, setTags] = useState(initial.tags.join(", "));
  const [imageUrl, setImageUrl] = useState(initial.imageUrl);
  const [coverMode, setCoverMode] = useState<"url" | "upload">("url");
  const [coverFile, setCoverFile] = useState<File | null>(null);
  const [coverFocal, setCoverFocal] = useState<{ x: number; y: number } | null>(null);
  const [visibility, setVisibility] = useState<Visibility>(initial.visibility ?? "public");
  const [memberLimit, setMemberLimit] = useState(initial.memberLimit ? String(initial.memberLimit) : "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  return (
    <form
      className="bg-white rounded-xl border border-gray-200 p-6 space-y-4"
      onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true);
        setError(null);
        try {
          await onSaved({
            name,
            description,
            location,
            // Upload mode with a new file replaces any URL; otherwise keep it.
            imageUrl: coverMode === "upload" && coverFile ? "" : imageUrl,
            coverFile: coverMode === "upload" ? coverFile : null,
            coverFocal: coverMode === "upload" ? coverFocal ?? undefined : undefined,
            tags: tags
              .split(",")
              .map((t) => t.trim())
              .filter(Boolean),
            visibility,
            memberLimit: memberLimit.trim() ? Number(memberLimit) : null,
          });
        } catch (err) {
          setError(err instanceof Error ? err.message : "Save failed");
        } finally {
          setBusy(false);
        }
      }}
    >
      {error && <div className="text-red-600 text-sm">{error}</div>}
      <div>
        <label className="label" htmlFor="group-name">Group name</label>
        <input id="group-name" className="input" value={name} onChange={(e) => setName(e.target.value)} required />
      </div>
      <div>
        <label className="label">Description</label>
        <textarea
          className="input"
          rows={4}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </div>
      <div>
        <label className="label">Location</label>
        <input
          className="input"
          value={location}
          onChange={(e) => setLocation(e.target.value)}
          placeholder="e.g. Room 201, Main Campus"
        />
      </div>
      <div>
        <label className="label">Tags (comma separated)</label>
        <input
          className="input"
          value={tags}
          onChange={(e) => setTags(e.target.value)}
          placeholder="e.g. young-adults, bible-study"
        />
      </div>
      <div>
        <label className="label" htmlFor="member-limit">Member limit (optional)</label>
        <input
          id="member-limit"
          className="input"
          type="number"
          min={1}
          step={1}
          value={memberLimit}
          onChange={(e) => setMemberLimit(e.target.value)}
          placeholder="No limit"
        />
        <p className="text-xs text-gray-500 mt-1">
          For a room with a fixed capacity. When it's full, people can't join on their own; leaders can still add someone.
        </p>
      </div>
      <fieldset>
        <legend className="label">Who can see this group</legend>
        <div className="space-y-2 text-sm">
          <label className="flex items-start gap-2">
            <input
              type="radio"
              name="visibility"
              className="mt-1"
              checked={visibility === "public"}
              onChange={() => setVisibility("public")}
            />
            <span>
              <span className="font-semibold">Public</span>
              <span className="block text-gray-500">Anyone can see it, including people who are logged out.</span>
            </span>
          </label>
          <label className="flex items-start gap-2">
            <input
              type="radio"
              name="visibility"
              className="mt-1"
              checked={visibility === "private"}
              onChange={() => setVisibility("private")}
            />
            <span>
              <span className="font-semibold">Private</span>
              <span className="block text-gray-500">Only signed-in people can see it.</span>
            </span>
          </label>
          <label className="flex items-start gap-2">
            <input
              type="radio"
              name="visibility"
              className="mt-1"
              checked={visibility === "invite_only"}
              onChange={() => setVisibility("invite_only")}
            />
            <span>
              <span className="font-semibold">Invite only</span>
              <span className="block text-gray-500">
                Hidden from everyone except its members and church admins. People join only when a leader adds them.
              </span>
            </span>
          </label>
        </div>
      </fieldset>
      <div>
        <label className="label">Cover image</label>
        <div className="flex gap-2 mb-2">
          {(["url", "upload"] as const).map((m) => (
            <button
              key={m}
              type="button"
              onClick={() => setCoverMode(m)}
              className={`text-sm px-3 py-1 rounded-full border ${
                coverMode === m
                  ? "bg-brand text-white border-brand"
                  : "text-gray-600 border-gray-300 hover:border-brand"
              }`}
            >
              {m === "url" ? "Image URL" : "Upload"}
            </button>
          ))}
        </div>
        {coverMode === "url" ? (
          <input
            className="input"
            value={imageUrl}
            onChange={(e) => setImageUrl(e.target.value)}
            placeholder="https://…"
          />
        ) : (
          <ImageUpload
            label=""
            initialUrl={initial.imageUrl || undefined}
            initialFocal={{ x: initial.coverFocalX, y: initial.coverFocalY }}
            maxDim={1600}
            aspectClass="aspect-[16/9]"
            onFile={setCoverFile}
            onFocal={setCoverFocal}
          />
        )}
      </div>
      <div className="flex gap-3">
        <button type="submit" disabled={busy} className="btn-primary">
          {busy ? "Saving…" : saveLabel}
        </button>
        <button type="button" onClick={onCancel} className="btn-secondary">
          Cancel
        </button>
      </div>
    </form>
  );
}

// Exported so /groups/new can reuse the same form.
export function CreateGroupForm({ onSaved, onCancel }: {
  onSaved: (data: GroupFormData) => Promise<void>;
  onCancel: () => void;
}) {
  return (
    <GroupForm
      initial={{
        name: "", description: "", location: "", tags: [], imageUrl: "",
        coverFocalX: 50, coverFocalY: 50, visibility: "public",
      }}
      saveLabel="Create group"
      onSaved={onSaved}
      onCancel={onCancel}
    />
  );
}

export function GroupDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { user, loading: authLoading, isAdmin } = useAuth();
  const [detail, setDetail] = useState<GroupDetailResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [notFound, setNotFound] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [tab, setTab] = useState<Tab>("events");
  const [editing, setEditing] = useState(false);
  const [joinBusy, setJoinBusy] = useState(false);
  const [coverBusy, setCoverBusy] = useState(false);
  const [memberEmail, setMemberEmail] = useState("");
  const [memberBusy, setMemberBusy] = useState(false);
  const [memberError, setMemberError] = useState<string | null>(null);
  const coverRef = useRef<HTMLInputElement>(null);

  const load = useCallback(async () => {
    if (!id) return;
    setLoading(true);
    setError(null);
    setNotFound(false);
    try {
      const d = await Groups.get(id);
      setDetail(d);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) setNotFound(true);
      else setError(e instanceof Error ? e.message : "Failed to load group");
    } finally {
      setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    load();
  }, [load]);

  if (loading || authLoading) {
    return <div className="max-w-6xl mx-auto px-4 py-12 text-gray-500">Loading…</div>;
  }
  if (notFound || !detail) {
    return (
      <div className="max-w-6xl mx-auto px-4 py-12 text-center">
        <h1 className="text-2xl font-bold mb-2">Group not found</h1>
        <p className="text-gray-600">
          {error ?? "This group doesn't exist or isn't visible to you."}{" "}
          <Link to="/" className="text-brand font-semibold">
            Back home
          </Link>
        </p>
      </div>
    );
  }

  const { group, members, upcomingMeetings, canManage, isMember } = detail;
  const pendingReturns = detail.pendingReturns ?? [];
  // Server serializes an empty upcomingMeetings as null, not [].
  const meetings: MeetingInstance[] = upcomingMeetings ?? [];
  // Leadership comes from membership roles (several leaders allowed).
  const leaderCount = (members ?? []).filter((m) => m.role === "leader").length;
  const isLeader = !!user && (members ?? []).some((m) => m.id === user.id && m.role === "leader");

  async function toggleJoin() {
    if (!user) {
      navigate("/login", { state: { from: `/groups/${id}` } });
      return;
    }
    setJoinBusy(true);
    try {
      if (isMember) await Groups.leave(id!);
      else await Groups.join(id!);
      await load();
    } catch (e) {
      alert(e instanceof Error ? e.message : "Failed");
    } finally {
      setJoinBusy(false);
    }
  }

  function handleRsvp(meetingId: number, date: string, status: "going" | "maybe" | "declined") {
    setDetail((d) => {
      if (!d) return d;
      return {
        ...d,
        upcomingMeetings: (d.upcomingMeetings ?? []).map((m) => {
          if (m.meetingId !== meetingId || m.date !== date || m.myRsvp === status) return m;
          const rsvps = { ...m.rsvps };
          if (m.myRsvp) rsvps[m.myRsvp] = Math.max(0, (rsvps[m.myRsvp] ?? 0) - 1);
          rsvps[status] = (rsvps[status] ?? 0) + 1;
          return { ...m, myRsvp: status, rsvps };
        }),
      };
    });
  }

  function handleCancelled(meetingId: number, date: string) {
    setDetail((d) =>
      d
        ? {
            ...d,
            upcomingMeetings: (d.upcomingMeetings ?? []).filter(
              (m) => !(m.meetingId === meetingId && m.date === date)
            ),
          }
        : d
    );
  }

  async function saveEdit(data: GroupFormData) {
    // Upload first. Sending imageUrl: "" before the upload clears the
    // current cover, and a failed upload would leave it blank.
    if (data.coverFile) {
      await GroupCover.upload(id!, data.coverFile);
    }
    await Groups.update(id!, {
      name: data.name,
      description: data.description,
      tags: data.tags,
      location: data.location,
      ...(data.coverFile ? {} : { imageUrl: data.imageUrl }),
      ...(data.coverFocal ? { coverFocal: data.coverFocal } : {}),
      visibility: data.visibility,
      memberLimit: data.memberLimit,
    });
    setEditing(false);
    await load();
  }

  async function setRole(memberId: number, name: string, role: "leader" | "member") {
    const question = role === "leader"
      ? `Make ${name} a leader of “${group.name}”? Church admins will be notified.`
      : `Remove ${name} as a leader of “${group.name}”?`;
    if (!confirm(question)) return;
    setMemberBusy(true);
    setMemberError(null);
    try {
      await Groups.setRole(id!, memberId, role);
      await load();
    } catch (e) {
      setMemberError(e instanceof Error ? e.message : "Could not change the role");
    } finally {
      setMemberBusy(false);
    }
  }

  async function removeMember(memberId: number, name: string) {
    if (!confirm(`Remove ${name} from “${group.name}”? They can't rejoin on their own; a leader can add them back.`)) return;
    const note = prompt("Optional note for the church admins (never shown to the person you remove):") ?? "";
    setMemberBusy(true);
    setMemberError(null);
    try {
      await Groups.removeMember(id!, memberId, note.trim() || undefined);
      await load();
    } catch (e) {
      setMemberError(e instanceof Error ? e.message : "Could not remove that person");
    } finally {
      setMemberBusy(false);
    }
  }

  async function addMember(e: React.FormEvent) {
    e.preventDefault();
    const email = memberEmail.trim();
    if (!email) return;
    setMemberBusy(true);
    setMemberError(null);
    try {
      try {
        await Groups.addMember(id!, email);
      } catch (err) {
        // Full group: going over the limit is an explicit, audited choice (DEC-39).
        if (!(err instanceof ApiError) || err.status !== 409) throw err;
        if (!confirm(`“${group.name}” is full (${group.memberCount} of ${group.memberLimit}). Add ${email} anyway?`)) return;
        await Groups.addMember(id!, email, true);
      }
      setMemberEmail("");
      await load();
    } catch (e) {
      setMemberError(e instanceof Error ? e.message : "Could not add that person");
    } finally {
      setMemberBusy(false);
    }
  }

  async function decideReturn(memberId: number, name: string, decision: "approve" | "decline") {
    const question = decision === "approve"
      ? `Let ${name} back into “${group.name}”? This can put the group over its limit.`
      : `Decline ${name}'s return? They'll be removed from “${group.name}”.`;
    if (!confirm(question)) return;
    setMemberBusy(true);
    setMemberError(null);
    try {
      await Groups.decideReturn(id!, memberId, decision);
      await load();
    } catch (e) {
      setMemberError(e instanceof Error ? e.message : "Could not save that");
    } finally {
      setMemberBusy(false);
    }
  }

  async function requestDeletion() {
    if (!confirm("Ask an admin to delete this group? It stays up until they approve.")) return;
    setMemberBusy(true);
    setMemberError(null);
    try {
      await Groups.requestDeletion(id!);
      await load();
    } catch (e) {
      setMemberError(e instanceof Error ? e.message : "Could not request deletion");
    } finally {
      setMemberBusy(false);
    }
  }

  async function cancelDeletion() {
    setMemberBusy(true);
    setMemberError(null);
    try {
      await Groups.cancelDeletion(id!);
      await load();
    } catch (e) {
      setMemberError(e instanceof Error ? e.message : "Could not cancel the request");
    } finally {
      setMemberBusy(false);
    }
  }

  async function uploadCover(file: File) {
    setCoverBusy(true);
    try {
      const resized = await prepareUpload(file, 1600);
      await GroupCover.upload(id!, resized);
      await load();
    } catch (e) {
      alert(e instanceof Error ? e.message : "Cover upload failed");
    } finally {
      setCoverBusy(false);
    }
  }

  return (
    <div>
      {/* Cover header */}
      <div className="relative h-56 sm:h-72 overflow-hidden bg-gray-800">
        <img
          src={coverImage(group.imageUrl)}
          alt=""
          className="w-full h-full object-cover"
          style={{ objectPosition: focalPosition(group.coverFocalX, group.coverFocalY) }}
        />
        <div className="absolute inset-0 bg-gradient-to-t from-black/60 to-transparent" />
        <div className="absolute bottom-0 left-0 right-0">
          <div className="max-w-6xl mx-auto px-4 pb-5 flex items-end gap-4 flex-wrap">
            <div className="grow min-w-0">
              {(group.status === "pending" || group.visibility !== "public") && (
                <div className="flex flex-wrap gap-2 mb-2">
                  {group.status === "pending" && (
                    <span className="inline-block text-xs font-bold uppercase tracking-wide bg-yellow-100 text-yellow-800 rounded-full px-3 py-1">
                      Pending approval
                    </span>
                  )}
                  {group.visibility === "private" && (
                    <span className="inline-block text-xs font-bold uppercase tracking-wide bg-white/90 text-gray-800 rounded-full px-3 py-1">
                      Private
                    </span>
                  )}
                  {group.visibility === "invite_only" && (
                    <span className="inline-block text-xs font-bold uppercase tracking-wide bg-gray-900 text-white rounded-full px-3 py-1">
                      Invite only
                    </span>
                  )}
                </div>
              )}
              <h1 className="text-3xl sm:text-4xl font-extrabold text-white">{group.name}</h1>
              <div className="text-white/80 text-sm mt-1">
                {canManage && group.memberLimit
                  ? `${group.memberCount} of ${group.memberLimit} members`
                  : `${group.memberCount} ${group.memberCount === 1 ? "member" : "members"}`}
                {group.leaderName ? ` · Organized by ${group.leaderName}` : ""}
                {group.location ? ` · ${group.location}` : ""}
              </div>
            </div>
            <div className="flex gap-2">
              {canManage && (
                <>
                  <Link
                    to={`/groups/${group.id}/meetings/new`}
                    className="btn-primary !bg-white !text-brand hover:!bg-gray-100"
                  >
                    Schedule event
                  </Link>
                  <button onClick={() => setEditing(true)} className="btn-secondary">
                    Edit group
                  </button>
                  <button
                    onClick={() => coverRef.current?.click()}
                    disabled={coverBusy}
                    className="btn-secondary"
                    title="Upload a cover photo (JPEG, PNG, GIF, WebP, up to 5MB)"
                  >
                    {coverBusy ? "…" : "Cover photo"}
                  </button>
                  <input
                    ref={coverRef}
                    type="file"
                    accept="image/jpeg,image/png,image/gif,image/webp"
                    className="hidden"
                    onChange={(e) => {
                      const f = e.target.files?.[0];
                      if (f) uploadCover(f);
                      e.target.value = "";
                    }}
                  />
                </>
              )}
              {(group.visibility !== "invite_only" || isMember) && (
                <button
                  onClick={toggleJoin}
                  disabled={joinBusy}
                  className={isMember ? "btn-secondary" : "btn-primary"}
                >
                  {joinBusy ? "…" : isMember ? "Leave group" : "Join this group"}
                </button>
              )}
            </div>
          </div>
        </div>
      </div>

      <div className="max-w-6xl mx-auto px-4 py-6">
        {editing ? (
          <GroupForm
            initial={{
              name: group.name,
              description: group.description,
              location: group.location,
              tags: group.tags,
              imageUrl: group.imageUrl,
              coverFocalX: group.coverFocalX,
              coverFocalY: group.coverFocalY,
              visibility: group.visibility,
              memberLimit: group.memberLimit,
            }}
            saveLabel="Save changes"
            onSaved={saveEdit}
            onCancel={() => setEditing(false)}
          />
        ) : (
          <>
            {/* Tabs */}
            <div className="border-b border-gray-200 mb-6">
              <nav className="flex gap-6">
                {(["events", "about", "members"] as Tab[]).map((t) => (
                  <button
                    key={t}
                    onClick={() => setTab(t)}
                    className={
                      tab === t
                        ? "pb-3 border-b-2 border-brand text-brand font-semibold capitalize"
                        : "pb-3 text-gray-600 hover:text-gray-900 font-medium capitalize"
                    }
                  >
                    {t === "events" ? "Upcoming events" : t}
                  </button>
                ))}
              </nav>
            </div>

            {tab === "events" && (
              <div className="space-y-3">
                {meetings.length === 0 && (
                  <div className="bg-white rounded-xl border border-gray-200 p-8 text-center text-gray-600">
                    No upcoming events scheduled.
                  </div>
                )}
                {meetings.map((m) => (
                  <EventRow
                    key={`${m.meetingId}-${m.date}`}
                    instance={m}
                    groupName={group.name}
                    canManage={canManage}
                    loggedIn={!!user}
                    onRsvp={handleRsvp}
                    onCancelled={handleCancelled}
                  />
                ))}
              </div>
            )}

            {tab === "about" && (
              <div className="bg-white rounded-xl border border-gray-200 p-6 max-w-3xl space-y-4">
                <div>
                  <h3 className="font-bold mb-1">About this group</h3>
                  <p className="text-gray-700 whitespace-pre-wrap">
                    {group.description || "No description yet."}
                  </p>
                </div>
                {group.location && (
                  <div>
                    <h3 className="font-bold mb-1">Location</h3>
                    <p className="text-gray-700">{group.location}</p>
                  </div>
                )}
                {group.tags.length > 0 && (
                  <div>
                    <h3 className="font-bold mb-1">Tags</h3>
                    <div className="flex flex-wrap gap-2">
                      {group.tags.map((t) => (
                        <Link
                          key={t}
                          to={`/?tag=${encodeURIComponent(t)}`}
                          className="text-sm bg-gray-100 text-gray-700 hover:bg-gray-200 rounded-full px-3 py-1"
                        >
                          {t}
                        </Link>
                      ))}
                    </div>
                  </div>
                )}
                <div>
                  <h3 className="font-bold mb-1">Next meeting</h3>
                  <p className="text-gray-700">{formatNextMeeting(group.nextMeetingAt)}</p>
                </div>
              </div>
            )}

            {tab === "members" && (
              <div className="bg-white rounded-xl border border-gray-200 p-6 max-w-3xl">
                {!user ? (
                  <p className="text-gray-600">
                    <Link to="/login" className="text-brand font-semibold">
                      Log in
                    </Link>{" "}
                    to see the member roster.
                  </p>
                ) : (
                  <>
                    {memberError && <div className="text-red-600 text-sm mb-3">{memberError}</div>}
                    {members && members.length > 0 ? (
                      <ul className="divide-y divide-gray-100">
                        {members.map((m) => (
                          <li key={m.id} className="py-3 flex items-center gap-3">
                            <Avatar
                              url={m.avatarUrl || undefined}
                              name={m.name}
                              focalX={m.avatarFocalX}
                              focalY={m.avatarFocalY}
                            />
                            <div className="grow min-w-0">
                              <div className="font-semibold">{m.name}
                                {m.admin && (
                                  <span className="ml-2 inline-block rounded-full bg-purple-100 px-2 py-0.5 text-xs font-semibold text-purple-800">
                                    Admin
                                  </span>
                                )}
                              </div>
                              <div className="text-sm text-gray-500 capitalize">{m.role}</div>
                            </div>
                            {canManage && m.role !== "leader" && (
                              <button
                                type="button"
                                onClick={() => setRole(m.id, m.name, "leader")}
                                disabled={memberBusy}
                                className="text-sm font-medium text-brand hover:underline"
                              >
                                Make leader
                              </button>
                            )}
                            {canManage && m.role === "leader" && leaderCount > 1 && (!m.admin || isAdmin) && (
                              <button
                                type="button"
                                onClick={() => setRole(m.id, m.name, "member")}
                                disabled={memberBusy}
                                className="text-sm font-medium text-gray-600 hover:underline"
                              >
                                Remove as leader
                              </button>
                            )}
                            {canManage && m.id !== user?.id && (m.role !== "leader" || leaderCount > 1) && (!m.admin || isAdmin) && (
                              <button
                                type="button"
                                onClick={() => removeMember(m.id, m.name)}
                                disabled={memberBusy}
                                className="text-sm font-medium text-red-600 hover:underline"
                              >
                                Remove
                              </button>
                            )}
                          </li>
                        ))}
                      </ul>
                    ) : (
                      <p className="text-gray-600">No members yet.</p>
                    )}
                    {canManage && pendingReturns.length > 0 && (
                      <section id="waiting" className="mt-6">
                        <h3 className="font-bold">Waiting to return</h3>
                        <p className="text-sm text-gray-500">
                          Their accounts were turned back on while the group was full.
                        </p>
                        <ul className="divide-y divide-gray-100">
                          {pendingReturns.map((m) => (
                            <li key={m.id} className="py-3 flex items-center gap-3">
                              <Avatar url={m.avatarUrl || undefined} name={m.name} focalX={m.avatarFocalX} focalY={m.avatarFocalY} />
                              <div className="grow min-w-0 font-semibold">{m.name}</div>
                              <button type="button" onClick={() => decideReturn(m.id, m.name, "approve")} disabled={memberBusy} className="text-sm font-medium text-brand hover:underline">
                                Approve
                              </button>
                              <button type="button" onClick={() => decideReturn(m.id, m.name, "decline")} disabled={memberBusy} className="text-sm font-medium text-red-600 hover:underline">
                                Decline
                              </button>
                            </li>
                          ))}
                        </ul>
                      </section>
                    )}
                    {canManage && (
                      <form onSubmit={addMember} className="mt-4 flex gap-2 flex-wrap items-end">
                        <div className="grow min-w-[12rem]">
                          <label className="label" htmlFor="add-member">Add a member by email</label>
                          <input
                            id="add-member"
                            className="input"
                            value={memberEmail}
                            onChange={(e) => setMemberEmail(e.target.value)}
                            type="email"
                            placeholder="name@example.com"
                            autoComplete="off"
                          />
                        </div>
                        <button
                          type="submit"
                          disabled={memberBusy || !memberEmail.trim()}
                          className="btn-secondary"
                        >
                          {memberBusy ? "…" : "Add"}
                        </button>
                      </form>
                    )}
                    {isLeader && (
                      <div className="mt-6 border-t border-gray-100 pt-4">
                        {group.memberCount <= 1 ? (
                          group.deletionRequested ? (
                            <div className="space-y-2">
                              <p className="text-sm text-gray-600">
                                An admin still has to approve deleting this group.
                              </p>
                              <button
                                type="button"
                                onClick={cancelDeletion}
                                disabled={memberBusy}
                                className="btn-secondary"
                              >
                                Cancel deletion request
                              </button>
                            </div>
                          ) : (
                            <button
                              type="button"
                              onClick={requestDeletion}
                              disabled={memberBusy}
                              className="btn-secondary !text-red-600 !border-red-200 hover:!border-red-400"
                            >
                              Request deletion
                            </button>
                          )
                        ) : (
                          <p className="text-sm text-gray-500">
                            Hand leadership to someone else. You can ask an admin to delete the group once you are the only member.
                          </p>
                        )}
                      </div>
                    )}
                  </>
                )}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}
