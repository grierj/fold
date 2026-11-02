import { useState } from "react";
import { useSearchParams } from "react-router-dom";
import { ApiError, Guests } from "../api";
import { formatWhen } from "../lib/when";
import type { GuestRegistration } from "../types";
import { errorText } from "../lib/errors";
import { Button, ButtonLink } from "../components/ui/Button";
import { FormError } from "../components/ui/Field";

// Redeem a guest registration verification link (M1.4). Opening the
// page confirms nothing: the button posts the token, so a mail
// scanner following the link cannot register anybody.
export function GuestConfirm() {
  const [params] = useSearchParams();
  const token = params.get("token") ?? "";
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [full, setFull] = useState(false);
  const [result, setResult] = useState<{ registration: GuestRegistration; manageToken: string } | null>(null);

  async function confirm() {
    setBusy(true);
    setError(null);
    setFull(false);
    try {
      setResult(await Guests.confirm(token));
    } catch (err) {
      if (err instanceof ApiError && err.data["full"] === true) {
        setFull(true);
      } else {
        setError(errorText(err, "We couldn't confirm your registration."));
      }
    } finally {
      setBusy(false);
    }
  }

  const reg = result?.registration;
  const waitlisted = reg?.status === "waitlisted";
  return (
    <div className="fold-auth">
      <h1 className="text-3xl font-bold mb-6">Confirm your registration</h1>
      <div className="fold-section">
        {full ? (
          <p className="text-sm text-text-muted">
            This event is full. The link has been used up, so it won&apos;t work again.
          </p>
        ) : reg && result ? (
          <>
            <p className="text-sm">
              {waitlisted ? (
                <>
                  You&apos;re on the waitlist for <strong>{reg.title}</strong>
                  {reg.waitlistPosition != null ? ` — position ${reg.waitlistPosition}` : ""}
                  {reg.groupName ? ` with ${reg.groupName}` : reg.hostedBy ? ` hosted by ${reg.hostedBy}` : ""},{" "}
                  {formatWhen(reg.startsAt)}. We&apos;ll email you if a spot opens.
                </>
              ) : (
                <>
                  You&apos;re registered for <strong>{reg.title}</strong>
                  {reg.groupName ? ` with ${reg.groupName}` : reg.hostedBy ? ` hosted by ${reg.hostedBy}` : ""},{" "}
                  {formatWhen(reg.startsAt)}.
                </>
              )}
            </p>
            {reg.location && <p className="text-sm text-text-muted mt-2">{reg.location}</p>}
            <p className="text-sm text-text-muted mt-2">
              We also emailed you this confirmation with a link to manage it if your plans change.
            </p>
            <div className="mt-4 flex flex-col gap-3 sm:flex-row sm:flex-wrap">
              <ButtonLink variant="secondary" to={`/guest/manage/${result.manageToken}`}>
                Manage or cancel
              </ButtonLink>
              <ButtonLink variant="link" to={`/events/${reg.eventId}/dates/${reg.date}`}>
                View the event
              </ButtonLink>
            </div>
          </>
        ) : (
          <>
            <p className="text-sm text-text-muted">
              This confirms the guest registration that was sent to your email. The link works
              once and expires in 24 hours.
            </p>
            {error && <FormError layout="mt-3">{error}</FormError>}
            <Button variant="primary" layout="w-full mt-4" onClick={() => void confirm()} disabled={busy || !token}>
              {busy ? "Confirming…" : "Confirm my registration"}
            </Button>
            {!token && <p className="text-sm text-text-muted mt-3">This page needs the link from your email.</p>}
          </>
        )}
      </div>
    </div>
  );
}
