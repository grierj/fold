import { useHousehold } from "../lib/useHousehold";
import { SeriesEditor } from "../components/SeriesEditor";
import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ApiError, Events, GroupCover, Groups, Invitations } from "../api";
import type { EventOccurrence, GroupDetailResponse, OpenedDecision, HouseholdPerson, Visibility, VisibilityImpact } from "../types";
import { useAuth } from "../auth";
import { DateBadge } from "../components/DateBadge";
import { Attendance } from "../components/Attendance";
import { Menu } from "../components/ui/Menu";
import { EventVisibilityControl, visibilityRank } from "../components/EventVisibility";
import { GroupVisibilityReview } from "../components/GroupVisibilityReview";
import { RsvpButtons } from "../components/RsvpButtons";
import { GuestRegistrationForm } from "../components/GuestRegistrationForm";
import { Avatar } from "../components/Avatar";
import { ImageUpload } from "../components/ImageUpload";
import { coverSrc } from "../lib/covers";
import { CalendarIcon } from "../components/icons";
import { ErrorNotice } from "../components/ErrorNotice";
import { Missing } from "./NotFound";
import { EmptyState } from "../components/ui/EmptyState";
import { Illustration } from "../components/ui/Illustration";
import { Skeleton } from "../components/ui/Skeleton";
import { focalPosition, prepareUpload } from "../lib/image";
import { formatTime, formatWhen, zoneNote } from "../lib/when";
import { errorText } from "../lib/errors";
import { Checkbox, Choices, Field, FormError, HiddenFileInput, Input, Radio, Textarea } from "../components/ui/Field";
import { FormActions } from "../components/ui/FormActions";
import { Button, ButtonLink } from "../components/ui/Button";

type Tab = "events" | "about" | "members";


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
      setError(errorText(err, "We couldn't update this date."));
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} className="mt-3 space-y-3 border-t border-border pt-3">
      <p className="text-sm text-text-muted">
        This date only. Later series changes leave it alone. The series date stays {instance.date}. {zoneNote(instance.tzid)}
      </p>
      {error && <FormError>{error}</FormError>}
      <div className="grid sm:grid-cols-3 gap-3">
        <Field id={`date-${instance.id}`} label="Date">
          <Input type="date" value={date} onChange={(e) => setDate(e.target.value)} required />
        </Field>
        <Field id={`time-${instance.id}`} label="Start time">
          <Input type="time" value={time} onChange={(e) => setTime(e.target.value)} required />
        </Field>
        <Field id={`dur-${instance.id}`} label="Minutes">
          <Input
            type="number"
            min={1}
            max={10080}
            value={duration}
            onChange={(e) => setDuration(Number(e.target.value))}
          />
        </Field>
      </div>
      <div className="grid sm:grid-cols-2 gap-3">
        <Field id={`title-${instance.id}`} label="Title">
          <Input value={title} onChange={(e) => setTitle(e.target.value)} />
        </Field>
        <Field id={`loc-${instance.id}`} label="Location">
          <Input value={location} onChange={(e) => setLocation(e.target.value)} />
        </Field>
      </div>
      <div className="flex gap-2">
        <Button variant="primary" type="submit" disabled={busy}>
          {busy ? "Saving…" : "Save this date"}
        </Button>
        <Button variant="secondary" onClick={onClose}>
          Close
        </Button>
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
  moreDates,
  loggedIn,
  onRsvp,
  people,
  onCancelled,
  onChanged,
}: {
  instance: EventOccurrence;
  groupName: string;
  groupVisibility: Visibility;
  canManage: boolean;
  showDescription: boolean;
  // A series shows its next date only, plus a link to the rest (DEC-63).
  moreDates: number;
  loggedIn: boolean;
  onRsvp: () => void;
  people: HouseholdPerson[];
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
      alert(errorText(e, "We couldn't cancel that date."));
    } finally {
      setCancelling(false);
    }
  }

  return (
    <div className="bg-surface rounded-lg border border-border p-4 flex gap-4">
      <DateBadge at={instance.startsAt} />
      <div className="grow min-w-0">
        <div className="font-bold text-lg">{name}</div>
        {showDescription && instance.description && (
          <p className="text-sm text-text-muted whitespace-pre-wrap mt-1">{instance.description}</p>
        )}
        <div className="text-sm text-text-muted">
          {time}
          {loc ? ` · ${loc}` : ""}
        </div>
        {instance.overridden && (
          <div className="text-sm text-text-muted">This date was edited on its own, so series changes leave it alone.</div>
        )}
        <Attendance instance={instance} />
        {moreDates > 0 && (
          <Link to={`/events/${instance.eventId}`} className="inline-flex min-h-11 items-center text-sm font-medium text-primary">
            {moreDates} more {moreDates === 1 ? "date" : "dates"} in the next 90 days
          </Link>
        )}
        {loggedIn && (
          <div className="mt-2">
            <RsvpButtons instance={instance} people={people} onChange={onRsvp} />
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
                childcareAvailable: instance.childcareAvailable, childcareCapacity: instance.childcareCapacity,
                childcareAges: instance.childcareAges, childcareNotes: instance.childcareNotes,
                remoteAvailable: instance.remoteAvailable,
                remoteCapacity: instance.remoteCapacity,
                videoProvider: instance.videoProvider,
                videoUrl: instance.videoUrl,
                videoReveal: instance.videoReveal,
              }}
              idPrefix={`${instance.id}`}
              locationHint="Leave it blank to use the group's location."
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
        <div className="shrink-0">
          <Menu
            tone="quiet"
            label={`More for ${name} on ${instance.date}`}
            items={[
              { label: "Edit this date", onSelect: () => setEditor(editor === "date" ? null : "date") },
              { label: instance.recurrenceRule ? "Edit series" : "Edit event", onSelect: () => setEditor(editor === "series" ? null : "series") },
              { label: cancelling ? "Cancelling…" : "Cancel this date", onSelect: () => void cancel(), danger: true, disabled: cancelling },
            ]}
          />
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
      className="bg-surface rounded-lg border border-border p-6 space-y-6"
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
          setError(errorText(err, "We couldn't save your changes."));
        } finally {
          setBusy(false);
        }
      }}
    >
      {error && <FormError>{error}</FormError>}
      <section className="space-y-4">
        <h2 className="text-base font-semibold">About the group</h2>
        <Field id="group-name" label="Group name">
          <Input value={name} onChange={(e) => setName(e.target.value)} required />
        </Field>
        <Field id="group-description" label="Description" optional>
          <Textarea rows={4} value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>
        <Field id="group-location" label="Location" optional hint="Where it usually meets, for example Room 201, Main Campus.">
          <Input value={location} onChange={(e) => setLocation(e.target.value)} />
        </Field>
        <Field id="group-tags" label="Tags" optional hint="Separate tags with commas, for example young-adults, bible-study.">
          <Input value={tags} onChange={(e) => setTags(e.target.value)} />
        </Field>
      </section>
      <section className="space-y-4">
        <h2 className="text-base font-semibold">Who can join</h2>
        <Field id="member-limit" label="Member limit" optional hint="For a room with a fixed capacity. When it's full, people can't join on their own; leaders can still add someone. Leave it blank for no limit.">
          <Input type="number" min={1} step={1} value={memberLimit} onChange={(e) => setMemberLimit(e.target.value)} layout="sm:w-44" />
        </Field>
        <Choices legend="Who can see this group">
          <Radio
            name="visibility"
            label="Public"
            description="Anyone can see it, including people who are logged out."
            checked={visibility === "public"}
            onChange={() => setVisibility("public")}
          />
          <Radio
            name="visibility"
            label="Private"
            description="Only signed-in people can see it."
            checked={visibility === "private"}
            onChange={() => setVisibility("private")}
          />
          <Radio
            name="visibility"
            label="Invite only"
            description="Hidden from everyone except its members and church admins. People join only when a leader adds them."
            checked={visibility === "invite_only"}
            onChange={() => setVisibility("invite_only")}
          />
        </Choices>
      </section>
      <section>
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
          <div className="mt-2">
            <Checkbox label="Remove the current cover" checked={removeCover} onChange={(e) => setRemoveCover(e.target.checked)} />
          </div>
        )}
      </section>
      <FormActions>
        <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={busy}>
          {busy ? "Saving…" : saveLabel}
        </Button>
        <Button variant="secondary" layout="w-full sm:w-auto" onClick={onCancel}>
          Cancel
        </Button>
      </FormActions>
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
  const people = useHousehold(user);
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

  // quiet refreshes in place (after an RSVP) without the Loading screen.
  const load = useCallback(async (quiet = false) => {
    if (!id) return;
    if (!quiet) setLoading(true);
    setError(null);
    setNotFound(false);
    try {
      const d = await Groups.get(id);
      setDetail(d);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) setNotFound(true);
      else setError(errorText(e, "We couldn't load this group."));
    } finally {
      setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    load();
  }, [load]);

  if (loading || authLoading) {
    return (
      <div className="max-w-6xl mx-auto px-4 py-8">
        <Skeleton kind="cover" />
        <div className="mt-6">
          <Skeleton kind="lines" />
        </div>
      </div>
    );
  }
  if (notFound) {
    return (
      <Missing
        title="Group not found"
        body="It may have been removed, or it may be private. If someone invited you, ask them for the link again."
      />
    );
  }
  if (!detail) {
    return (
      <div className="max-w-xl mx-auto px-4 py-12 sm:py-16">
        <ErrorNotice message={error ?? "We couldn't load this group."} onRetry={() => void load()} />
      </div>
    );
  }

  const { group, members, upcomingEvents, canManage, isMember } = detail;
  const events: EventOccurrence[] = upcomingEvents ?? [];
  // A series lists only its next date, plus "N more dates in the next 90
  // days" linking to the event page. The count is what GET /groups/:id
  // loaded: dates up to 90 days out (api_groups.go). A longer series has
  // more, and an open-ended one has no total, so the words name the window.
  const nextDates: EventOccurrence[] = [];
  const datesPerEvent = new Map<number, number>();
  for (const m of events) {
    if (!datesPerEvent.has(m.eventId)) nextDates.push(m);
    datesPerEvent.set(m.eventId, (datesPerEvent.get(m.eventId) ?? 0) + 1);
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
      alert(errorText(e, "That didn't work."));
    } finally {
      setJoinBusy(false);
    }
  }

  // Refetch the whole date after a batch; counts and mixed outcomes come from the server.
  function handleRsvp() { void load(true); }

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
        setReviewError(errorText(e, "We couldn't save your changes."));
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
      setMemberError(errorText(e, "We couldn't change that role."));
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
      setMemberError(errorText(e, "We couldn't remove that person."));
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
      setMemberError(errorText(e, "We couldn't add that person."));
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
      setMemberError(errorText(err, "We couldn't send the invitation."));
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
      setMemberError(errorText(e, "We couldn't send the deletion request."));
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
      setMemberError(errorText(e, "We couldn't cancel the request."));
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
      alert(errorText(e, "We couldn't upload the cover photo."));
    } finally {
      setCoverBusy(false);
    }
  }

  return (
    <div>
      {/* Cover header */}
      {/* No overflow-hidden: the More menu opens below the cover. */}
      <div className="relative h-56 sm:h-72 bg-inverse">
        <img
          src={coverSrc(group)}
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
                    <span className="inline-block text-xs font-bold uppercase tracking-wide bg-warning-subtle text-warning-on-subtle rounded-full px-3 py-1">
                      Pending approval
                    </span>
                  )}
                  {group.visibility === "private" && (
                    <span className="inline-block text-xs font-bold uppercase tracking-wide bg-surface/90 text-text rounded-full px-3 py-1">
                      Private
                    </span>
                  )}
                  {group.visibility === "invite_only" && (
                    <span className="inline-block text-xs font-bold uppercase tracking-wide bg-inverse text-inverse-foreground rounded-full px-3 py-1">
                      Invite only
                    </span>
                  )}
                </div>
              )}
              <h1 className="text-3xl sm:text-4xl font-bold text-white">{group.name}</h1>
              <div className="text-white/80 text-sm mt-1">
                {canManage && group.memberLimit
                  ? `${group.memberCount} of ${group.memberLimit} members`
                  : `${group.memberCount} ${group.memberCount === 1 ? "member" : "members"}`}
                {group.leaderName ? ` · Organized by ${group.leaderName}` : ""}
                {group.location ? ` · ${group.location}` : ""}
              </div>
            </div>
            <div className="flex flex-wrap gap-2">
              {canManage && (
                <>
                  <Link
                    to={`/groups/${group.id}/events/new`}
                    className="btn-primary bg-surface! text-primary! hover:bg-sunken!"
                  >
                    Schedule event
                  </Link>
                  {/* One primary action (Schedule event); the rest live in More (DEC-63). */}
                  <Menu
                    label="More group actions"
                    items={[
                      { label: "Edit group", onSelect: () => setEditing(true) },
                      { label: coverBusy ? "Uploading cover…" : "Change cover photo", onSelect: () => coverRef.current?.click(), disabled: coverBusy },
                      ...(isMember ? [{ label: joinBusy ? "Leaving…" : "Leave group", onSelect: () => void toggleJoin(), disabled: joinBusy }] : []),
                    ]}
                  />
                  <HiddenFileInput
                    ref={coverRef}
                    accept="image/jpeg,image/png,image/gif,image/webp"
                    onChange={(e) => {
                      const f = e.target.files?.[0];
                      if (f) uploadCover(f);
                      e.target.value = "";
                    }}
                  />
                </>
              )}
              {(group.visibility !== "invite_only" || isMember) && !(canManage && isMember) && (
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
            {reviewError && <FormError layout="mb-2">{reviewError}</FormError>}
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
            saveLabel="Save group"
            onSaved={saveEdit}
            onCancel={() => setEditing(false)}
          />
        ) : (
          <>
            {/* Tabs */}
            <div className="border-b border-border mb-6">
              <nav className="flex gap-6">
                {(["events", "about", "members"] as Tab[]).map((t) => (
                  <button
                    key={t}
                    onClick={() => setTab(t)}
                    className={
                      tab === t
                        ? "pb-3 border-b-2 border-primary text-primary font-semibold capitalize"
                        : "pb-3 text-text-muted hover:text-text font-medium capitalize"
                    }
                  >
                    {t === "events" ? "Upcoming events" : t}
                  </button>
                ))}
              </nav>
            </div>

            {tab === "events" && (
              <div className="space-y-3">
                {group.visibility === "public" && group.status === "active" && (
                  <div className="flex flex-wrap items-center gap-x-2">
                    <ButtonLink variant="link" to={`/cal/groups/${group.id}.ics`} reloadDocument>
                      Subscribe in your calendar
                    </ButtonLink>
                    <span className="text-sm text-text-muted">Adds this group&rsquo;s public events to your calendar app.</span>
                  </div>
                )}
                {events.length === 0 && (
                  <EmptyState
                    illustration={<Illustration name="no-events" />}
                    title="No upcoming events"
                    body={
                      canManage
                        ? "Schedule one so members can RSVP."
                        : "When a leader schedules something, it will show up here."
                    }
                    action={
                      canManage ? (
                        <ButtonLink to={`/groups/${group.id}/events/new`} variant="secondary" layout="w-full sm:w-auto">
                          Schedule event
                        </ButtonLink>
                      ) : undefined
                    }
                  />
                )}
                {nextDates.map((m) => (
                  <EventRow
                    key={`${m.eventId}-${m.date}`}
                    instance={m}
                    groupName={group.name}
                    groupVisibility={group.visibility}
                    canManage={canManage}
                    showDescription
                    moreDates={(datesPerEvent.get(m.eventId) ?? 1) - 1}
                    loggedIn={!!user}
                    onRsvp={handleRsvp}
            people={people}
                    onCancelled={handleCancelled}
                    onChanged={() => void load()}
                  />
                ))}
              </div>
            )}

            {tab === "about" && (
              <div className="bg-surface rounded-lg border border-border p-6 max-w-3xl space-y-4">
                <div>
                  <h3 className="font-bold mb-1">About this group</h3>
                  <p className="text-text-muted whitespace-pre-wrap">
                    {group.description || "No description yet."}
                  </p>
                </div>
                {group.location && (
                  <div>
                    <h3 className="font-bold mb-1">Location</h3>
                    <p className="text-text-muted">{group.location}</p>
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
                          className="text-sm bg-sunken text-text-muted hover:bg-border rounded-full px-3 py-1"
                        >
                          {t}
                        </Link>
                      ))}
                    </div>
                  </div>
                )}
                <div>
                  <h3 className="font-bold mb-1">Next event</h3>
                  <p className="text-text-muted">
                    {group.nextEventAt ? (
                      formatWhen(group.nextEventAt)
                    ) : (
                      <span className="inline-flex items-center gap-1">
                        <CalendarIcon className="h-4 w-4" />
                        No upcoming events
                      </span>
                    )}
                  </p>
                </div>
              </div>
            )}

            {tab === "members" && (
              <div className="bg-surface rounded-lg border border-border p-6 max-w-3xl">
                {!user ? (
                  <p className="text-text-muted">
                    <Link to="/login" className="text-primary font-semibold">
                      Log in
                    </Link>{" "}
                    to see the member roster.
                  </p>
                ) : (
                  <>
                    {memberError && <FormError layout="mb-3">{memberError}</FormError>}
                    {members && members.length > 0 ? (
                      <ul className="divide-y divide-border">
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
                                  <span className="ml-2 inline-block rounded-full bg-primary-subtle px-2 py-0.5 text-xs font-semibold text-primary-on-subtle">
                                    Admin
                                  </span>
                                )}
                              </div>
                              <div className="text-sm text-text-muted capitalize">{m.role}</div>
                            </div>
                            {canManage && (
                              <Menu
                                tone="quiet"
                                label={`More for ${m.name}`}
                                items={[
                                  ...(m.role !== "leader"
                                    ? [{ label: "Make leader", onSelect: () => void setRole(m.id, m.name, "leader"), disabled: memberBusy }]
                                    : []),
                                  ...(m.role === "leader" && leaderCount > 1 && (!m.admin || isAdmin)
                                    ? [{ label: "Remove as leader", onSelect: () => void setRole(m.id, m.name, "member"), disabled: memberBusy }]
                                    : []),
                                  ...(m.id !== user?.id && (m.role !== "leader" || leaderCount > 1) && (!m.admin || isAdmin)
                                    ? [{ label: "Remove from group", onSelect: () => void removeMember(m.id, m.name), danger: true, disabled: memberBusy }]
                                    : []),
                                ]}
                              />
                            )}
                          </li>
                        ))}
                      </ul>
                    ) : (
                      <p className="text-text-muted">No members yet.</p>
                    )}
                    {canManage && (
                      <>
                      <form onSubmit={inviteMember} className="mt-4 flex gap-2 flex-wrap items-end">
                        <div className="grow min-w-48">
                          <Field id="invite-member" label="Invite someone to Fold">
                            <Input
                              value={inviteEmail}
                              onChange={(e) => setInviteEmail(e.target.value)}
                              type="email"
                              placeholder="name@example.com"
                              autoComplete="off"
                            />
                          </Field>
                        </div>
                        <Button variant="secondary" type="submit" disabled={inviteBusy || !inviteEmail.trim()}>
                          {inviteBusy ? "Sending…" : "Send invite"}
                        </Button>
                        {group.memberLimit != null && group.memberCount >= group.memberLimit && (
                          <div className="w-full">
                            <Checkbox
                              label="Invite over the limit anyway"
                              description="This group is full. Going over the limit is logged."
                              checked={inviteOverLimit}
                              onChange={(e) => setInviteOverLimit(e.target.checked)}
                            />
                          </div>
                        )}
                      </form>
                      {inviteStatus && <p className="text-success text-sm mt-2">{inviteStatus}</p>}
                      <form onSubmit={addMember} className="mt-4 flex gap-2 flex-wrap items-end">
                        <div className="grow min-w-48">
                          <Field id="add-member" label="Add a member by email">
                            <Input
                              value={memberEmail}
                              onChange={(e) => setMemberEmail(e.target.value)}
                              type="email"
                              placeholder="name@example.com"
                              autoComplete="off"
                            />
                          </Field>
                        </div>
                        <Button variant="secondary" type="submit" disabled={memberBusy || !memberEmail.trim()}>
                          {memberBusy ? "Adding…" : "Add member"}
                        </Button>
                      </form>
                      </>
                    )}
                    {isLeader && (
                      <div className="mt-6 border-t border-border pt-4">
                        {group.memberCount <= 1 ? (
                          group.deletionRequested ? (
                            <div className="space-y-2">
                              <p className="text-sm text-text-muted">
                                An admin still has to approve deleting this group.
                              </p>
                              <Button variant="secondary" onClick={cancelDeletion} disabled={memberBusy}>
                                Cancel deletion request
                              </Button>
                            </div>
                          ) : (
                            <Button variant="danger-outline" onClick={requestDeletion} disabled={memberBusy}>
                              Request deletion
                            </Button>
                          )
                        ) : (
                          <p className="text-sm text-text-muted">
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
