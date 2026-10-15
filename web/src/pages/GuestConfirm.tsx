import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { ApiError, Guests } from "../api";
import { formatNextEvent } from "../lib/wall";
import type { GuestRegistration } from "../types";

// Redeem a guest registration verification link (M1.4). Opening the
// page confirms nothing: the button posts the token, so a mail
// scanner following the link cannot register anybody.
export function GuestConfirm() {
  const [params] = useSearchParams();
  const token = params.get("token") ?? "";
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<{ registration: GuestRegistration; manageToken: string } | null>(null);

  async function confirm() {
    setBusy(true);
    setError(null);
    try {
      setResult(await Guests.confirm(token));
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "That link is invalid or has expired.");
    } finally {
      setBusy(false);
    }
  }

  const reg = result?.registration;
  return (
    <div className="max-w-md mx-auto px-4 py-12">
      <h1 className="text-3xl font-extrabold mb-6">Confirm your registration</h1>
      <div className="bg-white rounded-xl border border-gray-200 p-6">
        {reg && result ? (
          <>
            <p className="text-sm">
              You&rsquo;re registered for <strong>{reg.title}</strong>
              {reg.groupName ? ` with ${reg.groupName}` : reg.hostedBy ? ` hosted by ${reg.hostedBy}` : ""},{" "}
              {formatNextEvent(reg.startsLocal, reg.tzid, null)}.
            </p>
            {reg.location && <p className="text-sm text-gray-600 mt-2">{reg.location}</p>}
            <p className="text-sm text-gray-600 mt-2">
              We also emailed you this confirmation with a link to cancel if you can&rsquo;t come.
            </p>
            <p className="mt-4 flex gap-4">
              <Link to={`/guest/manage/${result.manageToken}`} className="text-brand font-semibold text-sm">
                Manage or cancel
              </Link>
              <Link to={`/events/${reg.eventId}`} className="text-brand font-semibold text-sm">
                View the event
              </Link>
            </p>
          </>
        ) : (
          <>
            <p className="text-sm text-gray-700">
              This confirms the guest registration that was sent to your email. The link works
              once and expires in 24 hours.
            </p>
            {error && <p className="text-red-600 text-sm mt-3">{error}</p>}
            <button
              type="button"
              onClick={() => void confirm()}
              disabled={busy || !token}
              className="btn-primary w-full mt-4"
            >
              {busy ? "Confirming…" : "Confirm my registration"}
            </button>
            {!token && <p className="text-sm text-gray-500 mt-3">This page needs the link from your email.</p>}
          </>
        )}
      </div>
    </div>
  );
}
