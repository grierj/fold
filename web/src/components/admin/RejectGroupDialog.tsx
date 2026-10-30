import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { errorCode, errorText } from "../../lib/errors";
import { Button } from "../ui/Button";
import { Field, FormError, Textarea } from "../ui/Field";
import { FormActions } from "../ui/FormActions";

// Rejecting a group asks first and lets the admin say why (DEC-96). Solid
// danger is right here: destruction is the whole point of this dialog (DEC-63).
// The message reaches the leaders as notification text, so the server refuses
// an email address or phone number; that comes back as an error under the
// field and the dialog stays open with what was typed.
const FIELD_ERRORS = new Set(["too_long", "message_has_contact"]);

export function RejectGroupDialog({ groupName, onReject, onCancel }: {
  groupName: string;
  // Throws when the server says no; the dialog shows why and stays open.
  onReject: (message: string) => Promise<void>;
  onCancel: () => void;
}) {
  const titleId = useId();
  const panel = useRef<HTMLDivElement>(null);
  const overlay = useRef<HTMLDivElement>(null);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [fieldError, setFieldError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  // A modal, like SeriesRsvpDialog: the overlay sits in <body>, everything
  // else is inert while it is open, and focus returns to the opener on close.
  useEffect(() => {
    const returnTo = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const behind = Array.from(document.body.children).filter((el) => el !== overlay.current && !el.hasAttribute("inert"));
    behind.forEach((el) => el.setAttribute("inert", ""));
    panel.current?.querySelector<HTMLElement>("textarea")?.focus();
    return () => {
      behind.forEach((el) => el.removeAttribute("inert"));
      // The opener may be gone by now (the card leaves the list once rejected);
      // the page then moves focus to its status line instead.
      if (returnTo?.isConnected) returnTo.focus();
    };
  }, []);

  function keyDown(event: React.KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      if (!busy) onCancel();
      return;
    }
    if (event.key !== "Tab") return;
    const controls = Array.from(panel.current?.querySelectorAll<HTMLElement>("button:not([disabled]), textarea") ?? []);
    if (!controls.length) return;
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

  async function reject() {
    setBusy(true);
    setFieldError(null);
    setError(null);
    try {
      await onReject(message.trim());
    } catch (e) {
      const code = errorCode(e);
      const text = errorText(e, "We couldn't reject that.");
      if (code && FIELD_ERRORS.has(code)) setFieldError(text);
      else setError(text);
      setBusy(false);
    }
  }

  return createPortal(
    <div
      ref={overlay}
      className="fixed inset-0 z-50 flex items-end sm:items-center justify-center bg-black/40 p-0 sm:p-4"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget && !busy) onCancel();
      }}
    >
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onKeyDown={keyDown}
        className="w-full sm:max-w-md rounded-t-2xl sm:rounded-2xl bg-surface border border-border p-5 shadow-xl space-y-4"
      >
        <h2 id={titleId} className="text-xl font-bold">Reject “{groupName}”?</h2>
        <p className="text-sm text-text-muted">The group is archived and its leaders are told.</p>
        <Field
          label="Message to the leader"
          optional
          hint="They'll see this in Fold. Leave out email addresses and phone numbers."
          error={fieldError ?? undefined}
        >
          <Textarea
            value={message}
            onChange={(event) => { setMessage(event.target.value); setFieldError(null); }}
            maxLength={500}
            rows={4}
          />
        </Field>
        {error && <FormError>{error}</FormError>}
        <FormActions>
          <Button variant="danger" layout="w-full sm:w-auto" onClick={reject} disabled={busy}>
            {busy ? "Rejecting…" : "Reject group"}
          </Button>
          <Button variant="secondary" layout="w-full sm:w-auto" onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
        </FormActions>
      </div>
    </div>,
    document.body,
  );
}
