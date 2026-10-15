import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ApiError, Guests } from "../api";
import { formatNextEvent } from "../lib/wall";
import type { GuestRegistration } from "../types";

// The guest manage link from the confirmation email (M1.4). The token
// in the address is the credential, and it stops working once the
// date has passed.
export function GuestManage() {
  const { token = "" } = useParams<{ token: string }>();
  const [reg, setReg] = useState<GuestRegistration | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const res = await Guests.getManage(token);
      setReg(res.registration);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "That link is invalid or has expired.");
    }
  }, [token]);

  useEffect(() => {
    void load();
  }, [load]);

  async function cancel() {
    if (!reg || !confirm(`Cancel your registration for ${reg.title}?`)) return;
    setBusy(true);
    setError(null);
    try {
      const res = await Guests.cancelManage(token);
      setReg(res.registration);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not cancel your registration.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="max-w-md mx-auto px-4 py-12">
      <h1 className="text-3xl font-extrabold mb-6">Your guest registration</h1>
      <div className="bg-white rounded-xl border border-gray-200 p-6">
        {!reg && !error && <p className="text-sm text-gray-500">Loading…</p>}
        {error && <p className="text-sm text-red-600">{error}</p>}
        {reg && (
          <>
            <p className="text-sm">
              <strong>{reg.title}</strong>
              {reg.groupName ? ` with ${reg.groupName}` : reg.hostedBy ? ` hosted by ${reg.hostedBy}` : ""}
            </p>
            <p className="text-sm text-gray-600 mt-1">{formatNextEvent(reg.startsLocal, reg.tzid, null)}</p>
            {reg.location && <p className="text-sm text-gray-600">{reg.location}</p>}
            {reg.status === "cancelled" ? (
              <>
                <p className="text-sm mt-4">Your registration is cancelled.</p>
                <p className="mt-4">
                  <Link to={`/events/${reg.eventId}`} className="text-brand font-semibold text-sm">
                    Changed your mind? Register again
                  </Link>
                </p>
              </>
            ) : (
              <>
                <p className="text-sm mt-4">You&rsquo;re registered as going.</p>
                <button type="button" onClick={() => void cancel()} disabled={busy} className="btn-primary w-full mt-4">
                  {busy ? "Cancelling…" : "Cancel my registration"}
                </button>
                <p className="mt-4">
                  <Link to={`/events/${reg.eventId}`} className="text-brand font-semibold text-sm">
                    View the event
                  </Link>
                </p>
              </>
            )}
          </>
        )}
      </div>
    </div>
  );
}
