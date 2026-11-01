import { useId, type ReactNode } from "react";
import type { Visibility } from "../types";
import { ChevronDownIcon } from "./icons";
import { ChildcareField, type ChildcareValue } from "./ChildcareField";
import { RemoteField, type RemoteValue } from "./RemoteField";
import { SeatsField, type SeatsValue } from "./SeatsField";
import { VideoField, type VideoValue } from "./VideoField";
import { ReminderField, reminderSummary } from "./ReminderField";
import { Button } from "./ui/Button";
import { usePushEnabled } from "../lib/usePushEnabled";

const VISIBILITY_NAMES: Record<Visibility, string> = { public: "Public", private: "Private", invite_only: "Invite only" };

// The one line a closed "More options" shows, so nothing inside is hidden by
// surprise: "Visible to: Public · No seat limit · No video · No childcare".
export function optionsSummary({
  visibility,
  seats,
  video,
  remote,
  childcare,
  reminder,
  reminderPush,
}: {
  visibility?: Visibility;
  seats: SeatsValue;
  video: VideoValue;
  remote: RemoteValue;
  childcare: ChildcareValue;
  reminder?: number;
  // The push reminder switch, when the form offers it (M3.13d3).
  reminderPush?: boolean;
}): string {
  const cap = seats.capacity.trim();
  const seatText = cap === "" ? "No seat limit" : `${cap} ${cap === "1" ? "seat" : "seats"}${seats.waitlist ? " with waitlist" : ""}`;
  const parts = [
    ...(visibility ? [`Visible to: ${VISIBILITY_NAMES[visibility]}`] : []),
    seatText,
    video.provider === "none" ? "No video" : "Video link",
    ...(remote.available ? ["Remote seats"] : []),
    childcare.available ? "Childcare" : "No childcare",
    ...(reminder !== undefined ? [reminderSummary(reminder, reminderPush)] : []),
  ];
  return parts.join(" · ");
}

// A disclosure for the less common choices (DEC-95). It is controlled so a
// form can open it when an error lands inside. The fields mount only while it
// is open; their values live in the form, so closing never loses an edit, and
// Form's own checks don't try to focus a field nobody can see.
function MoreOptions({
  open,
  onOpenChange,
  summary,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  summary: string;
  children: ReactNode;
}) {
  const regionId = useId();
  const summaryId = useId();
  return (
    <div className="border-t border-border pt-4">
      <Button
        variant="link"
        aria-expanded={open}
        aria-controls={regionId}
        aria-describedby={open ? undefined : summaryId}
        onClick={() => onOpenChange(!open)}
      >
        More options
        <ChevronDownIcon className={`ml-1 size-5 ${open ? "rotate-180" : ""}`} />
      </Button>
      {!open && (
        <p id={summaryId} className="text-sm text-text-muted">
          {summary}
        </p>
      )}
      <div id={regionId} hidden={!open} className="mt-3 space-y-4">
        {open && children}
      </div>
    </div>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  const id = useId();
  return (
    <div role="group" aria-labelledby={id} className="space-y-3 border-t border-border pt-4">
      <h3 id={id} className="text-base font-semibold">
        {title}
      </h3>
      {children}
    </div>
  );
}

// Everything inside More options, in one order for the group, church-wide and
// Edit series forms: who can see it (a create form passes the shared
// VisibilityField; Edit series sets visibility in its own panel), seats and
// waitlist, Online (video and remote together), childcare and the reminders
// (the email, M3.6b, and the push reminder, M3.13d3).
export function EventOptions({
  idPrefix,
  open,
  onOpenChange,
  visibility,
  seats,
  onSeats,
  seatsError,
  video,
  onVideo,
  videoError,
  remote,
  onRemote,
  remoteError,
  childcare,
  onChildcare,
  childcareError,
  reminder,
  onReminder,
  reminderPush,
  onReminderPush,
}: {
  idPrefix: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  visibility?: { value: Visibility; field: ReactNode };
  seats: SeatsValue;
  onSeats: (next: SeatsValue) => void;
  seatsError?: string | null;
  video: VideoValue;
  onVideo: (next: VideoValue) => void;
  videoError?: string | null;
  remote: RemoteValue;
  onRemote: (next: RemoteValue) => void;
  remoteError?: string | null;
  childcare: ChildcareValue;
  onChildcare: (next: ChildcareValue) => void;
  childcareError?: string | null;
  reminder: number;
  onReminder: (minutes: number) => void;
  // The 2-hour push reminder switch (M3.13d3); hidden while the church has
  // notifications on devices turned off.
  reminderPush?: boolean;
  onReminderPush?: (on: boolean) => void;
}) {
  const pushOn = usePushEnabled();
  const push = pushOn && reminderPush !== undefined && onReminderPush ? { value: reminderPush, onChange: onReminderPush } : undefined;
  return (
    <MoreOptions
      open={open}
      onOpenChange={onOpenChange}
      summary={optionsSummary({ visibility: visibility?.value, seats, video, remote, childcare, reminder, reminderPush: push?.value })}
    >
      {visibility?.field}
      <Section title="Seats">
        <SeatsField idPrefix={idPrefix} value={seats} onChange={onSeats} error={seatsError} />
      </Section>
      <Section title="Online">
        <VideoField idPrefix={idPrefix} value={video} onChange={onVideo} error={videoError} />
        <RemoteField idPrefix={idPrefix} value={remote} onChange={onRemote} error={remoteError} />
      </Section>
      <Section title="Childcare">
        <ChildcareField idPrefix={idPrefix} value={childcare} onChange={onChildcare} error={childcareError} />
      </Section>
      <Section title="Reminders">
        <ReminderField idPrefix={idPrefix} value={reminder} onChange={onReminder} push={push} />
      </Section>
    </MoreOptions>
  );
}
