import { useId, type ReactNode } from "react";
import type { Visibility } from "../types";
import { ChevronDownIcon } from "./icons";
import { ChildcareField } from "./ChildcareField";
import { RemoteField } from "./RemoteField";
import { SeatsField } from "./SeatsField";
import { VideoField } from "./VideoField";
import type { EventOptionValues, OptionErrors } from "./eventOptionValues";
import { ReminderField, reminderSummary } from "./ReminderField";
import { Button } from "./ui/Button";
import { usePushEnabled } from "../lib/useSite";

const VISIBILITY_NAMES: Record<Visibility, string> = { public: "Public", private: "Private", invite_only: "Invite only" };

// The one line a closed "More options" shows, so nothing inside is hidden by
// surprise: "Visible to: Public · No seat limit · No video · No childcare".
function optionsSummary(v: EventOptionValues, visibility?: Visibility, pushOn = true): string {
  const { seats, video, remote, childcare, reminder } = v;
  const cap = seats.capacity.trim();
  const seatText = cap === "" ? "No seat limit" : `${cap} ${cap === "1" ? "seat" : "seats"}${seats.waitlist ? " with waitlist" : ""}`;
  const parts = [
    ...(visibility ? [`Visible to: ${VISIBILITY_NAMES[visibility]}`] : []),
    seatText,
    video.provider === "none" ? "No video" : "Video link",
    ...(remote.available ? ["Remote seats"] : []),
    childcare.available ? "Childcare" : "No childcare",
    reminderSummary(reminder, pushOn ? v.reminderPush : undefined),
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
  values,
  onChange,
  errors = {},
}: {
  idPrefix: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  visibility?: { value: Visibility; field: ReactNode };
  values: EventOptionValues;
  onChange: (next: EventOptionValues) => void;
  errors?: OptionErrors;
}) {
  // The 2-hour push reminder switch (M3.13d3) is hidden while the church has
  // notifications on devices turned off.
  const pushOn = usePushEnabled();
  const set = <K extends keyof EventOptionValues>(key: K) => (next: EventOptionValues[K]) => onChange({ ...values, [key]: next });
  return (
    <MoreOptions open={open} onOpenChange={onOpenChange} summary={optionsSummary(values, visibility?.value, pushOn)}>
      {visibility?.field}
      <Section title="Seats">
        <SeatsField idPrefix={idPrefix} value={values.seats} onChange={set("seats")} error={errors.seats} />
      </Section>
      <Section title="Online">
        <VideoField idPrefix={idPrefix} value={values.video} onChange={set("video")} error={errors.video} />
        <RemoteField idPrefix={idPrefix} value={values.remote} onChange={set("remote")} error={errors.remote} />
      </Section>
      <Section title="Childcare">
        <ChildcareField idPrefix={idPrefix} value={values.childcare} onChange={set("childcare")} error={errors.childcare} />
      </Section>
      <Section title="Reminders">
        <ReminderField idPrefix={idPrefix} value={values.reminder} onChange={set("reminder")} push={pushOn ? { value: values.reminderPush, onChange: set("reminderPush") } : undefined} />
      </Section>
    </MoreOptions>
  );
}
