import { useRef, useState } from "react";
import { AdminGuardianLinks } from "../api";
import type { GuardianLink } from "../types";
import { errorText } from "../lib/errors";
import { formatBirthDate } from "../lib/when";
import { Button } from "./ui/Button";
import { Field, FormError, Input, showFieldErrors, Textarea } from "./ui/Field";

// One pending household link on the admin Approvals tab (M1.9, DEC-32).
// Confirming needs a note on how the admin checked. An admin can't
// confirm their own link while another admin is active (DEC-79): that
// error shows here and the item stays. onDone gets a line to announce.
export function GuardianLinkCard({ link, onDone }: { link: GuardianLink; onDone: (announce: string) => void }) {
  const [checkNote, setCheckNote] = useState("");
  const [rejectNote, setRejectNote] = useState("");
  const [noteError, setNoteError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<"" | "confirm" | "reject">("");
  const ref = useRef<HTMLDivElement>(null);
  const who = `${link.personName}'s link to ${link.guardianName}`;

  async function confirmLink() {
    if (!checkNote.trim()) {
      showFieldErrors(ref.current, () => setNoteError("Write how you checked this link first."));
      return;
    }
    setNoteError(null);
    setBusy("confirm");
    setError(null);
    try {
      const { guardianLink } = await AdminGuardianLinks.confirm(link.id, checkNote.trim());
      const self = guardianLink.confirmedBy !== undefined && guardianLink.confirmedBy === link.guardianUserId;
      onDone(self ? `Confirmed ${who}. Recorded as a self-confirmation.` : `Confirmed ${who}.`);
    } catch (e) {
      setError(errorText(e, "We couldn't confirm that link."));
      setBusy("");
    }
  }

  async function rejectLink() {
    if (!confirm(`Don't confirm ${link.guardianName} as guardian of ${link.personName}?`)) return;
    setBusy("reject");
    setError(null);
    try {
      await AdminGuardianLinks.reject(link.id, rejectNote.trim() || undefined);
      onDone(`Didn't confirm ${who}.`);
    } catch (e) {
      setError(errorText(e, "We couldn't reject that link."));
      setBusy("");
    }
  }

  return (
    <div ref={ref} data-link-card className="bg-surface rounded-lg border border-border p-6">
      <div className="text-xl font-bold">{link.personName}</div>
      <div className="mt-1 flex flex-wrap gap-2">
        <span className="inline-block rounded-full bg-sunken px-2 py-0.5 text-xs font-semibold text-text-muted">
          {link.personKind === "teen" ? "Teen" : "Child"}
        </span>
      </div>
      <div className="text-sm text-text-muted mt-1">
        Guardian: {link.guardianName}
        {link.householdName ? ` · ${link.householdName}` : ""}
        {link.birthDate ? ` · born ${formatBirthDate(link.birthDate)}` : ""}
      </div>
      <div className="mt-4 space-y-4">
        <Field label="How did you check?" hint="For example: known family, parent came to the office." error={noteError ?? undefined}>
          <Textarea value={checkNote} onChange={(e) => setCheckNote(e.target.value)} rows={2} maxLength={500} />
        </Field>
        <Field label="Note if you don't confirm" optional>
          <Input value={rejectNote} onChange={(e) => setRejectNote(e.target.value)} maxLength={500} />
        </Field>
      </div>
      {error && <FormError layout="mt-2">{error}</FormError>}
      <div className="flex gap-3 mt-4">
        <Button variant="primary" onClick={confirmLink} disabled={!!busy}>{busy === "confirm" ? "Confirming…" : "Confirm link"}</Button>
        <Button variant="danger-outline" onClick={rejectLink} disabled={!!busy}>{busy === "reject" ? "Saving…" : "Don't confirm"}</Button>
      </div>
    </div>
  );
}
