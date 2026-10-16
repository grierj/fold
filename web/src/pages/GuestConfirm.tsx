import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { ApiError, Guests } from "../api";
import { formatWhen } from "../lib/when";
import type { GuestRegistration } from "../types";
import { errorText } from "../lib/errors";

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
    <div className="max-w-md mx-auto px-4 py-12">
      <h1 className="text-3xl font-bold mb-6">Confirm your registration</h1>
      <div className="bg-surface rounded-lg border border-border p-6">
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
            <p className="mt-4 flex gap-4">
              <Link to={`/guest/manage/${result.manageToken}`} className="text-primary font-semibold text-sm">
                Manage or cancel
              </Link>
              <Link to={`/events/${reg.eventId}`} className="text-primary font-semibold text-sm">
                View the event
              </Link>
            </p>
          </>
        ) : (
          <>
            <p className="text-sm text-text-muted">
              This confirms the guest registration that was sent to your email. The link works
              once and expires in 24 hours.
            </p>
            {error && <p className="text-danger text-sm mt-3">{error}</p>}
            <button
              type="button"
              onClick={() => void confirm()}
              disabled={busy || !token}
              className="btn-primary w-full mt-4"
            >
              {busy ? "Confirming…" : "Confirm my registration"}
            </button>
            {!token && <p className="text-sm text-text-muted mt-3">This page needs the link from your email.</p>}
          </>
        )}
      </div>
    </div>
  );
}
