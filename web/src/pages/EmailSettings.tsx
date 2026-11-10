import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { EmailSettingsLink } from "../api/notifications";
import type { EmailSettingsChange } from "../api/notifications";
import { errorCode, errorText } from "../lib/errors";
import { EmailSettings } from "../components/EmailSettings";
import { EmptyState } from "../components/ui/EmptyState";
import { Illustration } from "../components/ui/Illustration";
import { ButtonLink } from "../components/ui/Button";
import { ErrorNotice } from "../components/ErrorNotice";
import { Skeleton } from "../components/ui/Skeleton";
import type { NotificationSettings } from "../types";
import { useAuth } from "../auth";

// "You came here from …": one email of the kind the link came from.
const ONE_OF: Record<string, string> = {
  groups: "a group update",
  events: "an event update",
  messages: "a message from a leader",
  admin: "an admin notice",
};

// Shown above the switches when the link can't turn email back on. A refused
// turn-on has its own wording (ERROR_COPY.email_link_turn_on), which stands
// alone: it can arrive when this line was never shown.
const TURN_OFF_ONLY = "This link can turn email off, but not back on.";

// The email settings page an optional email's link opens (M3.3b, DEC-115).
// No sign-in: the token in the address is the authority. It shows the same
// switches as Me > Notifications.
export function EmailSettingsPage() {
  const { token = "" } = useParams<{ token: string }>();
  const { user } = useAuth();
  const [who, setWho] = useState<{ email: string; from: string; canTurnOn: boolean } | null>(null);
  const [gone, setGone] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  // The page's own first read is handed to the switches, so one visit is
  // one request; a retry inside the switches reads again.
  const first = useRef<NotificationSettings | null>(null);

  const check = useCallback(() => {
    setLoadError(null);
    EmailSettingsLink.get(token)
      .then((r) => {
        first.current = r.emailSettings;
        setWho({ email: r.email, from: r.fromCategory, canTurnOn: r.canTurnOn });
      })
      .catch((err) => {
        if (errorCode(err) === "not_found") setGone(true);
        else setLoadError(errorText(err, "We couldn't open your email settings."));
      });
  }, [token]);
  useEffect(() => { check(); }, [check]);

  const load = useCallback(() => {
    const cached = first.current;
    first.current = null;
    return cached ? Promise.resolve(cached) : EmailSettingsLink.get(token).then((r) => r.emailSettings);
  }, [token]);
  const save = useCallback((c: EmailSettingsChange) => EmailSettingsLink.update(token, c).then((r) => r.emailSettings), [token]);

  return (
    <div className="fold-auth">
      <h1 className="text-3xl font-bold mb-2">Email settings</h1>
      {gone ? (
        <EmptyState illustration={<Illustration name="link-expired" />} title="This link doesn't work anymore"
          body="Sign in to change your email settings."
          action={<ButtonLink to="/login" variant="primary">Log in</ButtonLink>} />
      ) : loadError ? (
        <ErrorNotice message={loadError} onRetry={check} />
      ) : !who ? (
        <Skeleton kind="lines" count={3} label="Loading your email settings…" />
      ) : (
        <>
          <p className="text-text-muted">For {who.email}</p>
          {ONE_OF[who.from] && <p className="text-text-muted">You came here from {ONE_OF[who.from]}; it's listed first.</p>}
          <div className="mt-6">
            {who.canTurnOn === false && <p className="mb-4 text-sm text-text-muted">{TURN_OFF_ONLY}</p>}
            <EmailSettings load={load} save={save} first={who.from} />
          </div>
          <p className="mt-6 text-sm">{user
            ? <Link to="/profile/notifications" className="text-link">See all your settings</Link>
            : <Link to="/login" className="text-link">Sign in to see all your settings</Link>}</p>
        </>
      )}
    </div>
  );
}
