import { useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Profile as ProfileApi } from "../api";
import { useAuth } from "../auth";
import { ErrorNotice } from "../components/ErrorNotice";
import { Button, ButtonLink } from "../components/ui/Button";
import { EmptyState } from "../components/ui/EmptyState";
import { Form } from "../components/ui/Form";
import { ERROR_COPY, errorCode, errorText } from "../lib/errors";

export function CancelEmailChange() {
  const [params] = useSearchParams();
  const token = params.get("token") ?? "";

  return (
    <div className="fold-auth">
      <h1 className="text-3xl font-bold mb-6">Cancel email change</h1>
      <CancelEmailChangeForm key={token} token={token} />
    </div>
  );
}

function CancelEmailChangeForm({ token }: { token: string }) {
  const { refresh } = useAuth();
  const [busy, setBusy] = useState(false);
  const [cancelled, setCancelled] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await ProfileApi.cancelEmailChange(token);
      await refresh();
      setCancelled(true);
    } catch (err) {
      setError(errorCode(err) === "link_expired" ? ERROR_COPY.link_expired : errorText(err, "We couldn't cancel the email change."));
    } finally {
      setBusy(false);
    }
  }

  if (!token) {
    return (
      <EmptyState
        title="This cancellation link is incomplete"
        body="Use the full link from the notice sent to your current email address."
        action={<ButtonLink to="/forgot-password" variant="secondary">Reset your password</ButtonLink>}
      />
    );
  }

  if (cancelled) {
    return (
      <EmptyState
        title="Pending email change cancelled"
        body="Your sign-in email was not changed, and all sessions have ended. Sign in again when you're ready."
        action={<ButtonLink to="/forgot-password" variant="secondary">Reset your password</ButtonLink>}
      />
    );
  }

  return (
    <Form noValidate onSubmit={submit} className="fold-form fold-section">
      <p className="text-sm text-text-muted">
        This only cancels a pending email change. Confirming also ends all current sign-ins to protect your account.
      </p>
      {error && <ErrorNotice message={error} />}
      <Button variant="primary" layout="w-full" type="submit" disabled={busy}>
        {busy ? "Cancelling…" : "Cancel email change and sign out all sessions"}
      </Button>
    </Form>
  );
}
