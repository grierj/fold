import { useCallback, useEffect, useId, useRef, useState } from "react";
import { ApiError, Groups } from "../api";
import type { DepartureConfirmation, DepartureKeepableDate, DeparturePreview } from "../types";
import { errorCode, errorText } from "../lib/errors";
import { formatWhen } from "../lib/when";
import { Button } from "./ui/Button";
import { Checkbox, Choices, Field, FormError, Textarea } from "./ui/Field";
import { FormActions } from "./ui/FormActions";

function countLabel(count: number, singular: string, plural = `${singular}s`) {
  return `${count} ${count === 1 ? singular : plural}`;
}

function statusLabel(status: DepartureKeepableDate["status"]) {
  return status === "waitlisted" ? "Waitlisted" : status === "maybe" ? "Maybe" : "Going";
}

function reasonLabel(reason: DepartureKeepableDate["keepReason"], remove: boolean) {
  const person = remove ? "them" : "you";
  if (reason === "underway") return `Recommended to keep: this date is already underway for ${person}.`;
  if (reason === "cutoff") return `Recommended to keep: this date starts soon for ${person}.`;
  return `Optional: this is a public date ${remove ? "they" : "you"} chose.`;
}

export function GroupDepartureDialog({
  groupId,
  subjectId,
  allowNote = false,
  onCancel,
  onCompleted,
  onUnavailable,
  fallbackFocus,
}: {
  groupId: number | string;
  subjectId?: number;
  allowNote?: boolean;
  onCancel: () => void;
  onCompleted: (result: DepartureConfirmation) => void | Promise<void>;
  onUnavailable?: () => void | Promise<void>;
  fallbackFocus?: () => void;
}) {
  const titleId = useId();
  const descriptionId = useId();
  const panel = useRef<HTMLDivElement>(null);
  const cancelButton = useRef<HTMLButtonElement>(null);
  const [preview, setPreview] = useState<DeparturePreview | null>(null);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [note, setNote] = useState("");
  const noteRef = useRef("");
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState(false);
  const [unavailable, setUnavailable] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const onUnavailableRef = useRef(onUnavailable);
  onUnavailableRef.current = onUnavailable;
  const fallbackFocusRef = useRef(fallbackFocus);
  fallbackFocusRef.current = fallbackFocus;
  const remove = subjectId !== undefined;

  const fetchPreview = useCallback(async () => {
    const response = remove
      ? await Groups.memberDeparturePreview(groupId, subjectId)
      : await Groups.departurePreview(groupId);
    return response.departure;
  }, [groupId, remove, subjectId]);

  const showPreview = useCallback((next: DeparturePreview, message?: string) => {
    setPreview(next);
    setSelected(new Set(next.dates.filter((date) => date.defaultKeep).map((date) => date.occurrenceId)));
    setUnavailable(false);
    setError(message ?? null);
    setNote(noteRef.current);
  }, []);

  useEffect(() => {
    const returnTo = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    cancelButton.current?.focus();
    return () => {
      if (returnTo?.isConnected) returnTo.focus();
      else fallbackFocusRef.current?.();
    };
  }, []);

  useEffect(() => {
    if (pending) panel.current?.focus();
  }, [pending]);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    fetchPreview()
      .then((next) => {
        if (alive) showPreview(next);
      })
      .catch((err) => {
        if (!alive) return;
        setUnavailable(true);
        setError(errorText(err, "We couldn't load this departure. Close this window and refresh the page."));
        void onUnavailableRef.current?.();
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => { alive = false; };
  }, [fetchPreview, showPreview]);

  function close() {
    if (!pending) onCancel();
  }

  function keyDown(event: React.KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      close();
      return;
    }
    if (event.key !== "Tab") return;
    const controls = Array.from(panel.current?.querySelectorAll<HTMLElement>(
      'button:not([disabled]), input:not([disabled]), textarea:not([disabled])'
    ) ?? []).filter((control) => control.getAttribute("aria-hidden") !== "true");
    if (!controls.length) {
      event.preventDefault();
      panel.current?.focus();
      return;
    }
    const first = controls[0];
    const last = controls[controls.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  async function refreshAfterConflict(code: string) {
    setPreview(null);
    setUnavailable(false);
    setLoading(true);
    setError(null);
    try {
      const next = await fetchPreview();
      showPreview(
        next,
        code === "departure_stale"
          ? "The upcoming dates changed. We refreshed the choices and restored the safest defaults. Review them, then confirm again."
          : "Membership or leadership changed. We refreshed what you can do. Review the choices, then confirm again."
      );
      requestAnimationFrame(() => cancelButton.current?.focus());
    } catch {
      setUnavailable(true);
      setError("This departure isn't available anymore. Close this window to see the current membership.");
      await onUnavailableRef.current?.();
      requestAnimationFrame(() => cancelButton.current?.focus());
    } finally {
      setLoading(false);
    }
  }

  async function confirmDeparture() {
    if (!preview || pending) return;
    setPending(true);
    setError(null);
    try {
      const keepOccurrenceIds = preview.dates
        .filter((date) => selected.has(date.occurrenceId))
        .map((date) => date.occurrenceId);
      const response = remove
        ? await Groups.confirmMemberDeparture(groupId, subjectId, preview.snapshot, keepOccurrenceIds, allowNote ? note.trim() || undefined : undefined)
        : await Groups.confirmDeparture(groupId, preview.snapshot, keepOccurrenceIds);
      await onCompleted(response.departure);
    } catch (err) {
      const code = errorCode(err);
      if (code === "departure_stale" || code === "departure_unavailable" || (err instanceof ApiError && (err.status === 403 || err.status === 404))) {
        await refreshAfterConflict(code);
      } else {
        setError(errorText(err, `We couldn't ${remove ? "remove this person" : "leave this group"}. Nothing was changed. Try again.`));
      }
    } finally {
      setPending(false);
    }
  }

  const heading = preview
    ? preview.action === "leave" ? `Leave ${preview.groupName}?` : `Remove ${preview.subjectName}?`
    : unavailable ? "Departure unavailable" : "Review departure";
  const goingClause = preview && preview.goingCount > 0
    ? `, including ${countLabel(
      preview.goingCount,
      preview.action === "leave" ? "date you said Going to" : "date they said Going to",
      preview.action === "leave" ? "dates you said Going to" : "dates they said Going to"
    )}`
    : "";
  const description = preview?.affectedCount
    ? preview.action === "leave"
      ? `You'll be taken off ${countLabel(preview.affectedCount, "upcoming date")}${goingClause}. Your past attendance stays.`
      : `They'll be taken off ${countLabel(preview.affectedCount, "upcoming date")}${goingClause}, and told what was kept and ended. Their past attendance stays.`
    : preview ? (preview.action === "leave" ? "Your past attendance stays." : "Their past attendance stays.") : null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-end justify-center bg-black/40 p-0 sm:items-center sm:p-4"
      onMouseDown={(event) => { if (event.target === event.currentTarget) close(); }}
    >
      <div
        ref={panel}
        role="dialog"
        tabIndex={-1}
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description ? descriptionId : undefined}
        aria-busy={loading || pending || undefined}
        onKeyDown={keyDown}
        className="max-h-[90vh] w-full overflow-y-auto rounded-t-2xl border border-border bg-surface p-5 shadow-xl sm:max-w-lg sm:rounded-2xl"
      >
        <h2 id={titleId} className="text-xl font-bold">{heading}</h2>
        {description && <p id={descriptionId} className="mt-2 text-sm text-text-muted">{description}</p>}

        {loading && <p role="status" className="mt-4 text-sm text-text-muted">Refreshing the safest choices…</p>}
        {error && <div className="mt-4"><FormError>{error}</FormError></div>}

        {preview && !loading && (
          <div className="mt-4 space-y-4">
            {preview.dates.length > 0 && (
              <Choices
                legend="Dates to keep"
                hint={`${remove ? "Choose which dates they keep" : "Choose which dates you keep"} after leaving the group. Only these dates stay available; this does not keep access to the group or other dates.`}
              >
                {preview.dates.map((date) => (
                  <Checkbox
                    key={date.occurrenceId}
                    checked={selected.has(date.occurrenceId)}
                    disabled={pending}
                    onChange={(event) => {
                      setSelected((current) => {
                        const next = new Set(current);
                        if (event.target.checked) next.add(date.occurrenceId);
                        else next.delete(date.occurrenceId);
                        return next;
                      });
                    }}
                    label={`Keep ${date.title} — ${formatWhen(date.startsAt)}`}
                    description={`${statusLabel(date.status)}. ${reasonLabel(date.keepReason, remove)}`}
                  />
                ))}
              </Choices>
            )}

            {remove && allowNote && (
              <Field
                label="Note for church admins"
                optional
                hint="This stays in church records and is never shown to the person you remove."
              >
                <Textarea maxLength={500} rows={3} value={note} disabled={pending} onChange={(event) => { noteRef.current = event.target.value; setNote(event.target.value); }} />
              </Field>
            )}

            {preview.affectedCount > 0 && (
              <p role="status" className="text-sm text-text-muted">
                {selected.size > 0
                  ? `${countLabel(selected.size, "date")} will be kept. ${countLabel(preview.affectedCount - selected.size, "date")} will end.`
                  : `${countLabel(preview.affectedCount, "upcoming date")} will end.`}
              </p>
            )}
          </div>
        )}

        <div className="mt-5">
          <FormActions>
            <Button ref={cancelButton} variant="secondary" layout="w-full sm:w-auto" disabled={pending} onClick={close}>
              {preview?.action === "leave" ? "Stay" : "Cancel"}
            </Button>
            {preview && !loading && (
              <Button
                variant="danger"
                layout="w-full sm:w-auto"
                disabled={pending}
                aria-busy={pending || undefined}
                onClick={() => void confirmDeparture()}
              >
                {pending ? (remove ? "Removing…" : "Leaving…") : remove ? "Remove from group" : "Leave group"}
              </Button>
            )}
          </FormActions>
        </div>
      </div>
    </div>
  );
}
