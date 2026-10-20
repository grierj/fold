import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Guests } from "../api";
import { formatWhen } from "../lib/when";
import type { GuestRegistration } from "../types";
import { ERROR_COPY, errorCode, errorText } from "../lib/errors";
import { Button } from "../components/ui/Button";
import { FormError } from "../components/ui/Field";
import { EmptyState } from "../components/ui/EmptyState";
import { Illustration } from "../components/ui/Illustration";

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
      // An unknown token is a link that expired or was never valid.
      setError(errorCode(err) === "not_found" ? ERROR_COPY.link_expired : errorText(err, "We couldn't open your registration."));
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
      setError(errorText(err, "We couldn't cancel your registration."));
    } finally {
      setBusy(false);
    }
  }

  if (!reg && error) {
    return (
      <div className="max-w-md mx-auto px-4 py-12">
        <h1 className="text-3xl font-bold mb-6">Your guest registration</h1>
        <EmptyState
          illustration={<Illustration name="link-expired" />}
          title="This link can't be used"
          body={error}
        />
      </div>
    );
  }

  return (
    <div className="max-w-md mx-auto px-4 py-12">
      <h1 className="text-3xl font-bold mb-6">Your guest registration</h1>
      <div className="bg-surface rounded-lg border border-border p-6">
        {!reg && !error && <p className="text-sm text-text-muted">Loading…</p>}
        {reg && error && <FormError>{error}</FormError>}
        {reg && (
          <>
            <p className="text-sm">
              <strong>{reg.title}</strong>
              {reg.groupName ? ` with ${reg.groupName}` : reg.hostedBy ? ` hosted by ${reg.hostedBy}` : ""}
            </p>
            <p className="text-sm text-text-muted mt-1">{formatWhen(reg.startsAt)}</p>
            {reg.location && <p className="text-sm text-text-muted">{reg.location}</p>}
            {reg.status === "cancelled" ? (
              <>
                <p className="text-sm mt-4">Your registration is cancelled.</p>
                <p className="mt-4">
                  <Link to={`/events/${reg.eventId}`} className="text-primary font-semibold text-sm">
                    Changed your mind? Register again
                  </Link>
                </p>
              </>
            ) : (
              <>
                {reg.status === "waitlisted" ? (
                  <p className="text-sm mt-4">
                    You&apos;re on the waitlist{reg.waitlistPosition != null ? ` — position ${reg.waitlistPosition}` : ""}.
                    We&apos;ll email you if a spot opens.
                  </p>
                ) : (
                  <p className="text-sm mt-4">You&apos;re registered as going.</p>
                )}
                <Button variant="primary" layout="w-full mt-4" onClick={() => void cancel()} disabled={busy}>
                  {busy ? "Cancelling…" : "Cancel my registration"}
                </Button>
                <p className="mt-4">
                  <Link to={`/events/${reg.eventId}`} className="text-primary font-semibold text-sm">
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
