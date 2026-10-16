import { SeriesEditor } from "../components/SeriesEditor";
import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ApiError, Events, GroupCover, Groups, Invitations } from "../api";
import type { EventOccurrence, GroupDetailResponse, OpenedDecision, RegistrationResponse, Visibility, VisibilityImpact } from "../types";
import { useAuth } from "../auth";
import { DateBadge } from "../components/DateBadge";
import { AttendeeNames } from "../components/EventIntro";
import { EventVisibilityControl, visibilityRank } from "../components/EventVisibility";
import { GroupVisibilityReview } from "../components/GroupVisibilityReview";
import { RsvpButtons } from "../components/RsvpButtons";
import { GuestRegistrationForm } from "../components/GuestRegistrationForm";
import { Avatar } from "../components/Avatar";
import { ImageUpload } from "../components/ImageUpload";
import { coverImage } from "../components/GroupCard";
import { focalPosition, prepareUpload } from "../lib/image";
import { formatTime, formatWhen, zoneNote } from "../lib/when";

type Tab = "events" | "about" | "members";

function rsvpTotal(r: Record<string, number>, key: string): number {
  return r[key] ?? 0;
}


function DateEditor({
  instance,
  onSaved,
  onClose,
}: {
  instance: EventOccurrence;
  onSaved: () => void;
  onClose: () => void;
}) {
  const [date, setDate] = useState(instance.startsLocal.slice(0, 10));
  const [time, setTime] = useState(instance.startsLocal.slice(11, 16));
  const [duration, setDuration] = useState(instance.durationMinutes);
  const [title, setTitle] = useState(instance.title);
  const [location, setLocation] = useState(instance.location);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    const body: { startsLocal?: string; durationMinutes?: number; location?: string; title?: string } = {};
    const startsLocal = `${date}T${time}`;
    if (startsLocal !== instance.startsLocal) body.startsLocal = startsLocal;
    if (duration !== instance.durationMinutes) body.durationMinutes = duration;
    if (title !== instance.title) body.title = title;
    if (location !== instance.location) body.location = location;
    if (Object.keys(body).length === 0) {
      onClose();
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await Events.updateOccurrence(instance.eventId, instance.date, body);
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not update this date");
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} className="mt-3 space-y-3 border-t border-gray-100 pt-3">
      <p className="text-sm text-gray-600">
        This date only. Later series changes leave it alone. The series date stays {instance.date}. {zoneNote(instance.tzid)}
      </p>
      {error && <div className="text-sm text-red-600">{error}</div>}
      <div className="grid sm:grid-cols-3 gap-3">
        <div>
          <label className="label" htmlFor={`date-${instance.id}`}>
            Date
          </label>
          <input id={`date-${instance.id}`} type="date" className="input" value={date} onChange={(e) => setDate(e.target.value)} required />
        </div>
        <div>
          <label className="label" htmlFor={`time-${instance.id}`}>
            Start time
          </label>
          <input id={`time-${instance.id}`} type="time" className="input" value={time} onChange={(e) => setTime(e.target.value)} required />
        </div>
        <div>
          <label className="label" htmlFor={`dur-${instance.id}`}>
            Minutes
          </label>
          <input
            id={`dur-${instance.id}`}
            type="number"
            min={1}
            max={10080}
            className="input"
            value={duration}
            onChange={(e) => setDuration(Number(e.target.value))}
          />
        </div>
      </div>
      <div className="grid sm:grid-cols-2 gap-3">
        <div>
          <label className="label" htmlFor={`title-${instance.id}`}>
            Title
          </label>
          <input id={`title-${instance.id}`} className="input" value={title} onChange={(e) => setTitle(e.target.value)} />
        </div>
        <div>
          <label className="label" htmlFor={`loc-${instance.id}`}>
            Location
          </label>
          <input id={`loc-${instance.id}`} className="input" value={location} onChange={(e) => setLocation(e.target.value)} />
        </div>
      </div>
      <div className="flex gap-2">
        <button type="submit" disabled={busy} className="btn-primary">
          {busy ? "Saving…" : "Save this date"}
        </button>
        <button type="button" onClick={onClose} className="btn-secondary">
          Close
        </button>
      </div>
    </form>
  );
}


function EventRow({
  instance,
  groupName,
  groupVisibility,
  canManage,
  showDescription,
  loggedIn,
  onRsvp,
  onCancelled,
  onChanged,
}: {
  instance: EventOccurrence;
  groupName: string;
  groupVisibility: Visibility;
  canManage: boolean;
  showDescription: boolean;
  loggedIn: boolean;
  onRsvp: (eventId: number, date: string, reg: RegistrationResponse) => void;
  onCancelled: (eventId: number, date: string) => void;
  onChanged: () => void;
}) {
  const [cancelling, setCancelling] = useState(false);
  const [editor, setEditor] = useState<null | "date" | "series">(null);
  const time = formatTime(instance.startsAt);
  const loc = instance.location || "";
  const name = instance.title || groupName;

  async function cancel() {
    if (!confirm(`Cancel this date on ${instance.date}?`)) return;
    setCancelling(true);
    try {
      await Events.cancelOccurrence(instance.eventId, instance.date);
      onCancelled(instance.eventId, instance.date);
    } catch (e) {
      alert(e instanceof Error ? e.message : "Cancel failed");
    } finally {
      setCancelling(false);
    }
  }

  return (
    <div className="bg-white rounded-xl border border-gray-200 p-4 flex gap-4">
      <DateBadge at={instance.startsAt} />
      <div className="grow min-w-0">
        <div className="font-bold text-lg">{name}</div>
        {showDescription && instance.description && (
          <p className="text-sm text-gray-700 whitespace-pre-wrap mt-1">{instance.description}</p>
        )}
        <div className="text-sm text-gray-600">
          {time}
          {loc ? ` · ${loc}` : ""}
        </div>
        {instance.overridden && (
          <div className="text-sm text-gray-500">This date was edited on its own, so series changes leave it alone.</div>
        )}
        <div className="text-sm text-gray-500 mt-1">
          {rsvpTotal(instance.registrations, "going")} going · {rsvpTotal(instance.registrations, "maybe")} maybe ·{" "}
          {rsvpTotal(instance.registrations, "declined")} can't go
        </div>
        <AttendeeNames instance={instance} />
        {loggedIn && (
          <div className="mt-2">
            <RsvpButtons instance={instance} onChange={(reg) => onRsvp(instance.eventId, instance.date, reg)} />
          </div>
        )}
        {!loggedIn && instance.visibility === "public" && (
          <div className="mt-2">
            <GuestRegistrationForm instance={instance} />
          </div>
        )}
        {editor === "date" && <DateEditor instance={instance} onSaved={onChanged} onClose={() => setEditor(null)} />}
        {editor === "series" && (
          <>
            <SeriesEditor
              series={{
                eventId: instance.eventId,
                startsLocal: instance.seriesStartsLocal,
                durationMinutes: instance.seriesDurationMinutes,
                tzid: instance.tzid,
                title: instance.seriesTitle,
                description: instance.description,
                location: instance.seriesLocation,
                recurrenceRule: instance.recurrenceRule,
                rruleUntil: instance.rruleUntil,
                capacity: instance.capacity,
                waitlistEnabled: instance.waitlistEnabled,
                guestsAllowed: instance.guestsAllowed,
              }}
              idPrefix={`${instance.id}`}
              locationLabel="Location (blank = group's location)"
              onSaved={onChanged}
              onClose={() => setEditor(null)}
            />
            <EventVisibilityControl
              key={`${instance.eventId}-${instance.visibility}-${instance.leaderOpened}`}
              eventId={instance.eventId}
              groupName={groupName}
              groupVisibility={groupVisibility}
              visibility={instance.visibility ?? groupVisibility}
              leaderOpened={instance.leaderOpened}
              onSaved={onChanged}
            />
          </>
        )}
      </div>
      {canManage && (
        <div className="shrink-0 flex flex-col items-end gap-2">
          <button type="button" onClick={() => setEditor(editor === "date" ? null : "date")} className="text-sm text-brand font-medium">
            Edit this date
          </button>
          <button type="button" onClick={() => setEditor(editor === "series" ? null : "series")} className="text-sm text-brand font-medium">
            {instance.recurrenceRule ? "Edit series" : "Edit event"}
          </button>
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
  coverFile?: File | null;
  removeCover?: boolean;
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
  const [removeCover, setRemoveCover] = useState(false);
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
            coverFile,
            removeCover: !coverFile && removeCover,
            coverFocal: coverFocal ?? undefined,
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
        <ImageUpload
          label="Cover image"
          initialUrl={initial.imageUrl || undefined}
          initialFocal={{ x: initial.coverFocalX, y: initial.coverFocalY }}
          maxDim={1600}
          aspectClass="aspect-video"
          onFile={setCoverFile}
          onFocal={setCoverFocal}
        />
        {initial.imageUrl && !coverFile && (
          <label className="flex items-center gap-2 text-sm mt-2">
            <input type="checkbox" checked={removeCover} onChange={(e) => setRemoveCover(e.target.checked)} />
            Remove the current cover
          </label>
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
  const [review, setReview] = useState<{ data: GroupFormData; impact: VisibilityImpact } | null>(null);
  const [reviewBusy, setReviewBusy] = useState(false);
  const [reviewError, setReviewError] = useState<string | null>(null);
  const [joinBusy, setJoinBusy] = useState(false);
  const [coverBusy, setCoverBusy] = useState(false);
  const [memberEmail, setMemberEmail] = useState("");
  const [memberBusy, setMemberBusy] = useState(false);
  const [inviteEmail, setInviteEmail] = useState("");
  const [inviteBusy, setInviteBusy] = useState(false);
  const [inviteOverLimit, setInviteOverLimit] = useState(false);
  const [inviteStatus, setInviteStatus] = useState<string | null>(null);
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

  const { group, members, upcomingEvents, canManage, isMember } = detail;
  const events: EventOccurrence[] = upcomingEvents ?? [];
  // The series description shows only on its first listed date.
  const visibilityDate = new Map<number, string>();
  for (const m of events) {
    if (!visibilityDate.has(m.eventId)) visibilityDate.set(m.eventId, m.date);
  }
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

  function handleRsvp(eventId: number, date: string, reg: RegistrationResponse) {
    setDetail((d) => {
      if (!d) return d;
      return {
        ...d,
        upcomingEvents: (d.upcomingEvents ?? []).map((m) => {
          if (m.eventId !== eventId || m.date !== date) return m;
          const registrations = { ...m.registrations };
          const before = m.myRegistration;
          if (before && before !== reg.status) {
            registrations[before] = Math.max(0, (registrations[before] ?? 0) - 1);
          }
          if (!before || before !== reg.status) {
            registrations[reg.status] = (registrations[reg.status] ?? 0) + 1;
          }
          return {
            ...m,
            myRegistration: reg.status,
            myGuestCount: reg.guestCount,
            myWaitlistPosition: reg.waitlistPosition ?? undefined,
            registrations,
          };
        }),
      };
    });
  }

  function handleCancelled(eventId: number, date: string) {
    setDetail((d) =>
      d
        ? {
            ...d,
            upcomingEvents: (d.upcomingEvents ?? []).filter((m) => !(m.eventId === eventId && m.date === date)),
          }
        : d
    );
  }

  async function saveEdit(data: GroupFormData) {
    // Making the group more private can change its events (DEC-48): show
    // the leader the list and get a decision before anything is saved.
    if (visibilityRank(data.visibility) < visibilityRank(group.visibility)) {
      const impact = await Groups.visibilityImpact(id!, data.visibility);
      if (impact.tightened.length > 0 || impact.leaderOpened.length > 0) {
        setReview({ data, impact });
        return;
      }
    }
    await commitEdit(data);
  }

  async function confirmReview(decisions: Record<string, OpenedDecision>) {
    if (!review) return;
    setReviewBusy(true);
    setReviewError(null);
    try {
      await commitEdit(review.data, decisions);
      setReview(null);
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        // The events changed since the preview; show the new list.
        setReview({ data: review.data, impact: await Groups.visibilityImpact(id!, review.data.visibility) });
        setReviewError("The group's events changed. Please review them again.");
      } else {
        setReviewError(e instanceof Error ? e.message : "Save failed");
      }
    } finally {
      setReviewBusy(false);
    }
  }

  async function commitEdit(data: GroupFormData, openedEvents?: Record<string, OpenedDecision>) {
    if (data.coverFile) {
      await GroupCover.upload(id!, data.coverFile);
    } else if (data.removeCover) {
      await GroupCover.remove(id!);
    }
    await Groups.update(id!, {
      name: data.name,
      description: data.description,
      tags: data.tags,
      location: data.location,
      ...(data.coverFocal ? { coverFocal: data.coverFocal } : {}),
      visibility: data.visibility,
      memberLimit: data.memberLimit,
      ...(openedEvents ? { openedEvents } : {}),
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

  // Invite someone to Fold and into this group (M1.2, 5.10). Leaders
  // invite only into groups they lead; the person sets a password on
  // acceptance. Going over the member limit is an explicit, audited
  // choice (DEC-39).
  async function inviteMember(e: React.FormEvent) {
    e.preventDefault();
    const email = inviteEmail.trim();
    if (!email) return;
    setInviteBusy(true);
    setMemberError(null);
    setInviteStatus(null);
    try {
      await Invitations.create({ email, groupId: Number(id), overLimit: inviteOverLimit });
      setInviteEmail("");
      setInviteOverLimit(false);
      setInviteStatus(`Invitation sent to ${email}.`);
    } catch (err) {
      setMemberError(err instanceof Error ? err.message : "Could not send the invitation");
    } finally {
      setInviteBusy(false);
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
        <div className="absolute inset-0 bg-linear-to-t from-black/60 to-transparent" />
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
                    to={`/groups/${group.id}/events/new`}
                    className="btn-primary bg-white! text-brand! hover:bg-gray-100!"
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
        {editing && review && (
          <>
            {reviewError && <div className="text-red-600 text-sm mb-2">{reviewError}</div>}
            <GroupVisibilityReview
              key={JSON.stringify(review.impact)}
              impact={review.impact}
              busy={reviewBusy}
              onConfirm={(d) => void confirmReview(d)}
              onCancel={() => {
                setReview(null);
                setReviewError(null);
              }}
            />
          </>
        )}
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
                {events.length === 0 && (
                  <div className="bg-white rounded-xl border border-gray-200 p-8 text-center text-gray-600">
                    No upcoming events scheduled.
                  </div>
                )}
                {events.map((m) => (
                  <EventRow
                    key={`${m.eventId}-${m.date}`}
                    instance={m}
                    groupName={group.name}
                    groupVisibility={group.visibility}
                    canManage={canManage}
                    showDescription={visibilityDate.get(m.eventId) === m.date}
                    loggedIn={!!user}
                    onRsvp={handleRsvp}
                    onCancelled={handleCancelled}
                    onChanged={() => void load()}
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
                  <h3 className="font-bold mb-1">Next event</h3>
                  <p className="text-gray-700">
                    {group.nextEventAt ? formatWhen(group.nextEventAt) : "No events scheduled yet"}
                  </p>
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
                    {canManage && (
                      <>
                      <form onSubmit={inviteMember} className="mt-4 flex gap-2 flex-wrap items-end">
                        <div className="grow min-w-48">
                          <label className="label" htmlFor="invite-member">Invite someone to Fold</label>
                          <input
                            id="invite-member"
                            className="input"
                            value={inviteEmail}
                            onChange={(e) => setInviteEmail(e.target.value)}
                            type="email"
                            placeholder="name@example.com"
                            autoComplete="off"
                          />
                        </div>
                        <button
                          type="submit"
                          disabled={inviteBusy || !inviteEmail.trim()}
                          className="btn-secondary"
                        >
                          {inviteBusy ? "…" : "Invite"}
                        </button>
                        {group.memberLimit != null && group.memberCount >= group.memberLimit && (
                          <label className="text-xs text-gray-600 flex items-center gap-1.5 w-full">
                            <input
                              type="checkbox"
                              checked={inviteOverLimit}
                              onChange={(e) => setInviteOverLimit(e.target.checked)}
                            />
                            This group is full — invite over the limit anyway (audited)
                          </label>
                        )}
                      </form>
                      {inviteStatus && <p className="text-green-700 text-sm mt-2">{inviteStatus}</p>}
                      <form onSubmit={addMember} className="mt-4 flex gap-2 flex-wrap items-end">
                        <div className="grow min-w-48">
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
                      </>
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
                              className="btn-secondary text-red-600! border-red-200! hover:border-red-400!"
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
