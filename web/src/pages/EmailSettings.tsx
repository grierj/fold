import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { EmailSettingsLink } from "../api";
import type { EmailSettingsChange } from "../api";
import { errorCode, errorText } from "../lib/errors";
import { EmailSettings } from "../components/EmailSettings";
import { EmptyState } from "../components/ui/EmptyState";
import { Illustration } from "../components/ui/Illustration";
import { ButtonLink } from "../components/ui/Button";
import { ErrorNotice } from "../components/ErrorNotice";
import { Skeleton } from "../components/ui/Skeleton";
import type { NotificationSettings } from "../types";

// "You came here from …": one email of the kind the link came from.
const ONE_OF: Record<string, string> = {
  groups: "a group update",
  events: "an event update",
  messages: "a message alert",
  admin: "an admin notice",
};

// The email settings page an optional email's link opens (M3.3b, DEC-115).
// No sign-in: the token in the address is the authority. It shows the same
// switches as Me > Notifications.
export function EmailSettingsPage() {
  const { token = "" } = useParams<{ token: string }>();
  const [who, setWho] = useState<{ email: string; from: string } | null>(null);
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
        setWho({ email: r.email, from: r.fromCategory });
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
    <div className="max-w-xl mx-auto px-4 py-8">
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
            <EmailSettings load={load} save={save} first={who.from} />
          </div>
          <p className="mt-6 text-sm"><Link to="/login" className="text-link">Sign in to see all your settings</Link></p>
        </>
      )}
    </div>
  );
}
