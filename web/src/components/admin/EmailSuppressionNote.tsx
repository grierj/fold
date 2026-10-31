import type { EmailSuppression } from "../../types";
import { formatEventDay } from "../../lib/when";

// Why Fold stopped emailing someone (M3.2c, DEC-112, DEC-113), in words: the
// admin Users row shows it under the person's name, never as color alone.
function suppressionLabel(reason: EmailSuppression["reason"]): string {
  switch (reason) {
    case "hard_bounce":
      return "Email bouncing";
    case "complaint":
      return "Marked our email as spam";
    default:
      return "Email stopped on purpose";
  }
}

function explanation(s: EmailSuppression): string {
  const day = formatEventDay(s.since);
  switch (s.reason) {
    case "hard_bounce":
      return `Fold stopped emailing this address on ${day} because it bounced. If the address works again, clear this; if you changed their email, there's nothing to clear. If your church uses Postmark, reactivate the address there too.`;
    case "complaint":
      return "They marked Fold's email as spam, so optional email stopped. Account email, like password resets, still goes.";
    default:
      return `An admin stopped email to this address on ${day}.`;
  }
}

export function EmailSuppressionNote({ suppression }: { suppression: EmailSuppression }) {
  return (
    <div className="mt-2 text-xs">
      <p className="font-semibold text-text">{suppressionLabel(suppression.reason)}</p>
      <p className="text-text-muted max-w-prose">{explanation(suppression)}</p>
    </div>
  );
}
