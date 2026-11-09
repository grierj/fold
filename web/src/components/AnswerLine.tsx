import type { ReactNode } from "react";
import { CheckIcon } from "./icons";

// Standing confirmation under an RSVP control (DEC-92). Waitlisted, full and
// remote-pool states keep their own copy above the control. The event page and
// the date page share it.
export function answerLine(status: string | null | undefined): ReactNode {
  if (status === "going") return <><CheckIcon className="inline h-4 w-4 mr-1 align-text-bottom text-success-on-subtle" />You&apos;re going. See you there!</>;
  if (status === "maybe") return "You said maybe. Change it anytime.";
  if (status === "declined") return "Got it. Only the leaders see this.";
  return null;
}
