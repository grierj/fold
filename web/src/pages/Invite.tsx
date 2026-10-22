import { useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Invitations as InvitationsApi } from "../api";
import { useAuth } from "../auth";
import { errorCode, errorText } from "../lib/errors";
import { Field, FormError, Input } from "../components/ui/Field";
import { Button, ButtonLink } from "../components/ui/Button";
import { EmptyState } from "../components/ui/EmptyState";
import { Illustration } from "../components/ui/Illustration";
import { Skeleton } from "../components/ui/Skeleton";

const INVITE_EXPIRED = "This invitation has expired or was already used. Ask the person who invited you for a new one.";
import type { Invitation } from "../types";

// Accept an invitation (M1.2, 5.10). The person sets a password and gets an
// active account; no approval step. The token is single use and expires.
export function Invite() {
  const [params] = useSearchParams();
  const token = params.get("token") ?? "";
  const navigate = useNavigate();
  const { refresh } = useAuth();
  const [invitation, setInvitation] = useState<Invitation | null>(null);
  const [site, setSite] = useState("");
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [waiting, setWaiting] = useState(false);

  useEffect(() => {
    if (!token) {
      setLoading(false);
      return;
    }
    InvitationsApi.get(token)
      .then((res) => {
        setInvitation(res.invitation);
        setSite(res.site);
        setName(res.invitation.name);
      })
      .catch((e) =>
        setError(
          errorCode(e) === "not_found" || errorCode(e) === "link_expired"
            ? INVITE_EXPIRED
            : errorText(e, "We couldn't open this invitation.")
        )
      )
      .finally(() => setLoading(false));
  }, [token]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const result = await InvitationsApi.accept({ token, name, password });
      setPassword("");
      if (result.pendingConsent) { setWaiting(true); return; }
      await refresh();
      navigate("/", { replace: true });
    } catch (e) {
      // "That email already has an account" (email_taken) stays: the
      // person holding the invitation can act on it by signing in.
      setError(
        errorCode(e) === "link_expired"
          ? INVITE_EXPIRED
          : errorCode(e) === "email_taken"
            ? "That email already has an account. Sign in instead."
            : errorText(e, "We couldn't create your account.")
      );
    } finally {
      setBusy(false);
    }
  }

  if (loading) {
    return (
      <div className="max-w-md mx-auto px-4 py-12">
        <Skeleton kind="page" label="Loading invitation…" />
      </div>
    );
  }

  if (waiting) return <div className="max-w-md mx-auto px-4 py-12" role="status">
    <h1 className="text-3xl font-bold mb-4">Your account is waiting for consent</h1>
    <p>Your sign-ups have moved to your account. An admin must check your guardian link. You cannot sign in yet. Ask your church office about the next steps.</p>
  </div>;

  if (!token || (!invitation && error)) {
    return (
      <div className="max-w-md mx-auto px-4 py-12">
        <h1 className="text-3xl font-bold mb-6">You're invited</h1>
        <EmptyState
          illustration={<Illustration name="link-expired" />}
          level={2}
          title="This link can't be used"
          body={error ?? "This link is missing its token."}
          action={
            <ButtonLink to="/login" variant="secondary" layout="w-full sm:w-auto">
              Back to log in
            </ButtonLink>
          }
        />
      </div>
    );
  }

  return (
    <div className="max-w-md mx-auto px-4 py-12">
      <h1 className="text-3xl font-bold mb-2">You're invited to {site}</h1>
      {invitation?.conversion && <div className="mb-4 space-y-2">
        <p>Create your own account for {invitation.name}. Your past and upcoming sign-ups will move to your account when you accept. Group memberships do not move, so some private events may no longer be available.</p>
        {invitation.accountType === "teen" && <p>Your account will wait for consent and an admin guardian check. You cannot sign in yet.</p>}
      </div>}
      {invitation?.groupName && (
        <p className="text-text-muted mb-6">
          to join the group <span className="font-semibold">{invitation.groupName}</span>
        </p>
      )}
      {!invitation?.groupName && <div className="mb-6" />}
      <form onSubmit={submit} className="bg-surface rounded-lg border border-border p-6 space-y-4">
        {error && <FormError>{error}</FormError>}
        <Field id="invite-your-name" label="Your name">
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={100}
            readOnly={invitation?.conversion}
            required
          />
        </Field>
        <Field id="invite-email" label="Email (you'll sign in with it)">
          <Input type="email" value={invitation?.email ?? ""} readOnly />
        </Field>
        <Field id="inv-password" label="Choose a password" hint="Must be at least 10 characters.">
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="new-password"
            required
          />
        </Field>
        <Button variant="primary" layout="w-full" type="submit" disabled={busy}>
          {busy ? "Creating account…" : "Create my account"}
        </Button>
      </form>
    </div>
  );
}
