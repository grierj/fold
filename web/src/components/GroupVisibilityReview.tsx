import { useState } from "react";
import type { OpenedDecision, Visibility, VisibilityImpact } from "../types";
import { formatDayLong } from "../lib/when";
import { Button } from "./ui/Button";
import { Choices, Radio } from "./ui/Field";

const WORD: Record<Visibility, string> = {
  public: "public",
  private: "private",
  invite_only: "invite-only",
};

function people(n: number) {
  return n === 1 ? "1 person" : `${n} people`;
}

// GroupVisibilityReview shows a leader what making the group more private
// does to its events before anything is saved (DEC-48). Events that only
// inherited the group's visibility are tightened. Events a leader opened
// wider need a choice: keep open (logged) or match the group. Registrants
// who could no longer see a tightened event are cancelled and told.
// subject "event" reuses the same panel for one event made more private (the
// impact then lists only that event under tightened).
export function GroupVisibilityReview({
  impact,
  subject = "group",
  busy,
  onConfirm,
  onCancel,
}: {
  impact: VisibilityImpact;
  subject?: "group" | "event";
  busy: boolean;
  onConfirm: (decisions: Record<string, OpenedDecision>) => void;
  onCancel: () => void;
}) {
  const [decisions, setDecisions] = useState<Record<string, OpenedDecision>>({});
  const next = WORD[impact.visibility];
  const ready = impact.leaderOpened.every((e) => decisions[(e.decisionKey || String(e.id))]);
  const label = (title: string) => title || "Untitled event";
  const who = subject === "group" ? "the group" : "this event";

  return (
    <section
      role="dialog"
      aria-labelledby="vis-review-title"
      className="bg-surface rounded-lg border border-warning-border p-6 space-y-4 mb-4"
    >
      <h2 id="vis-review-title" className="text-lg font-bold">
        {subject === "group" ? `Making the group ${next} changes some events` : `Making this event ${next}`}
      </h2>
      {impact.tightened.length > 0 && (
        <div>
          <h3 className="font-semibold text-sm">{subject === "group" ? `These events will become ${next} too` : `This event will become ${next}`}</h3>
          <ul
            className="mt-1 text-sm list-disc pl-5"
            aria-label={subject === "group" ? "Events that will match the group" : "Event that will change"}
          >
            {impact.tightened.map((e) => (
              <li key={e.decisionKey || `${e.id}-${e.title}`}>
                {label(e.title)}{e.date ? ` · ${formatDayLong(e.date)}` : ""} <span className="text-text-muted">(now {WORD[e.visibility]})</span>
                {e.registrants > 0 && (
                  <span className="text-danger"> · {people(e.registrants)} registered will be cancelled and notified</span>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
      {impact.leaderOpened.length > 0 && (
        <fieldset>
          <legend className="font-semibold text-sm">A leader opened these events up. Choose for each one.</legend>
          <ul className="mt-2 space-y-3">
            {impact.leaderOpened.map((e) => {
              const key = (e.decisionKey || String(e.id));
              return (
                <li key={e.decisionKey || e.id} data-testid={`opened-${e.id}`}>
                  <Choices
                    legend={
                      <>
                        {label(e.title)}{e.date ? ` · ${formatDayLong(e.date)}` : ""} <span className="text-text-muted font-normal">(now {WORD[e.visibility]})</span>
                      </>
                    }
                  >
                    <Radio
                      name={`opened-${key}`}
                      label={`Keep it ${WORD[e.visibility]}`}
                      description="Logged, and other leaders and admins are told."
                      checked={decisions[key] === "keep"}
                      onChange={() => setDecisions((d) => ({ ...d, [key]: "keep" }))}
                    />
                    <Radio
                      name={`opened-${key}`}
                      label={`Make it ${next}`}
                      description={e.registrants > 0 && <span className="text-danger">Cancels {people(e.registrants)} and notifies them.</span>}
                      checked={decisions[key] === "tighten"}
                      onChange={() => setDecisions((d) => ({ ...d, [key]: "tighten" }))}
                    />
                  </Choices>
                </li>
              );
            })}
          </ul>
        </fieldset>
      )}
      <div className="flex gap-2">
        <Button variant="primary" disabled={busy || !ready} onClick={() => onConfirm(decisions)}>
          {busy ? "Saving…" : `Make ${who} ${next}`}
        </Button>
        <Button variant="secondary" disabled={busy} onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </section>
  );
}
