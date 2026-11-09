import { useState, type ReactNode } from "react";
import { OVERLAP_REASON_MAX } from "../lib/roomLabels";
import { AlertCircleIcon } from "./icons";
import { Button } from "./ui/Button";
import { Field, Textarea } from "./ui/Field";

// An admin's reason for double-booking a room: what they typed, and the check
// before it is sent. The server checks again.
export function useOverlapReason(reasonId: string) {
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  return {
    reason, setReason, error,
    reset() {
      setReason("");
      setError(null);
    },
    clearError: () => setError(null),
    // The trimmed reason, or null after showing what is wrong and moving focus there.
    read(): string | null {
      const why = reason.trim();
      const problem = !why ? "Say why you are double-booking this room." : why.length > OVERLAP_REASON_MAX ? `Use ${OVERLAP_REASON_MAX} characters or fewer.` : null;
      setError(problem);
      if (problem) document.getElementById(reasonId)?.focus();
      return problem ? null : why;
    },
  };
}

// The warning and reason box an admin sees when a room is in use. `message` says
// what is busy; Book anyway sends the booking again with the reason.
export function DoubleBookPrompt({ reasonId, message, prompt, busy, onBookAnyway }: {
  reasonId: string;
  message: ReactNode;
  prompt: ReturnType<typeof useOverlapReason>;
  busy: boolean;
  onBookAnyway: () => void;
}) {
  return (
    <div className="space-y-2 rounded-md border border-warning-border bg-warning-subtle p-3">
      <p role="alert" className="text-sm text-warning-on-subtle"><AlertCircleIcon className="mr-1 inline-block size-4 align-text-bottom" />{message}</p>
      <Field id={reasonId} label="Reason for double-booking" error={prompt.error}>
        <Textarea rows={2} maxLength={OVERLAP_REASON_MAX} value={prompt.reason} onChange={(e) => prompt.setReason(e.target.value)} />
      </Field>
      <Button variant="secondary" disabled={busy} onClick={onBookAnyway}>Book anyway</Button>
    </div>
  );
}
