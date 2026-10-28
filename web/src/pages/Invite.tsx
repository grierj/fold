import { Form } from "../components/ui/Form";
import { useEffect, useRef, useState } from "react";
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
import { AccountAgeFields } from "../components/AccountAgeFields";
import type { AgeBand, Invitation } from "../types";

// Accept a single-use invitation and choose a password. Household adults
// wait for admin approval.
export function Invite() {
  const [params] = useSearchParams();
  const token = params.get("token") ?? "";
  const navigate = useNavigate();
  const { refresh } = useAuth();
  const [invitation, setInvitation] = useState<Invitation | null>(null);
  const [site, setSite] = useState("");
  const [name, setName] = useState("");
  const [ageBand, setAgeBand] = useState<AgeBand>("adult");
  const [birthDate, setBirthDate] = useState("");
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
    if (!invitation?.conversion && ageBand === "under13") return;
    setBusy(true);
    setError(null);
    try {
      const result = await InvitationsApi.accept({ token, name, password, ageBand, birthDate: ageBand === "teen" ? birthDate : undefined });
      setPassword("");
      if (result.pendingApproval) { setWaiting(true); return; }
      await refresh();
      navigate(result.pendingConsent ? "/waiting-for-consent" : "/", { replace: true });
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

  if (waiting) return <AccountWaiting teen={invitation?.accountType === "teen"} />;

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
        <p>Create your own account for {invitation.name}. Your past and upcoming sign-ups will move to your account after an admin approves{invitation.accountType === "teen" ? ", consent is granted, and the new guardian link is confirmed" : ""}. Group memberships do not move, so some private events may no longer be available.</p>
        <p>A church admin needs to approve your account before you can sign in. Until then, nothing changes: your household keeps managing your sign-ups as before.</p>
      </div>}
      {invitation?.groupName && (
        <p className="text-text-muted mb-6">
          to join the group <span className="font-semibold">{invitation.groupName}</span>
        </p>
      )}
      {!invitation?.groupName && <div className="mb-6" />}
      <Form noValidate onSubmit={submit} className="bg-surface rounded-lg border border-border p-6 space-y-4">
        {error && <FormError>{error}</FormError>}
        <Field id="invite-your-name" label="Your name" hint={invitation?.conversion ? "You can change it later in your profile." : undefined}>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={100}
            readOnly={invitation?.conversion}
            required
          />
        </Field>
        <Field id="invite-email" label="Email (you'll sign in with it)" hint="Set by the person who invited you.">
          <Input type="email" value={invitation?.email ?? ""} readOnly />
        </Field>
        {!invitation?.conversion && <AccountAgeFields id="invite" ageBand={ageBand} birthDate={birthDate} onAgeBand={setAgeBand} onBirthDate={setBirthDate} />}
        <Field id="inv-password" label="Choose a password" hint="Must be at least 10 characters.">
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="new-password"
            required
          />
        </Field>
        <Button variant="primary" layout="w-full" type="submit" disabled={busy || (!invitation?.conversion && ageBand === "under13")}>
          {busy ? "Creating account…" : "Create my account"}
        </Button>
      </Form>
    </div>
  );
}

// Shown after an adult accepts. Focus moves to the
// heading so screen readers start here; no live region, because the whole
// page changed (PR 111 M1). Adults wait for approval (Warden, PR 111 F1).
function AccountWaiting({ teen = false }: { teen?: boolean }) {
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => heading.current?.focus(), []);
  return (
    <div className="max-w-md mx-auto px-4 py-12">
      <h1 ref={heading} tabIndex={-1} className="text-3xl font-bold mb-4 focus:outline-none">Your account is almost ready</h1>
      <p>A church admin needs to approve your account before you can sign in. Until then, nothing changes: your household keeps managing your sign-ups as before. We'll email you when it's approved. Then you can sign in with the password you chose.</p>
      {teen && <p className="mt-4">Your account also needs consent and a fresh guardian-link check. Your household keeps managing your sign-ups until those are complete.</p>}
    </div>
  );
}
