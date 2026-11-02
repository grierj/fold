import { Form } from "../components/ui/Form";
import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { AccountRequests } from "../api";
import { useAuth } from "../auth";
import { errorCode, errorText } from "../lib/errors";
import { Field, FormError, Input, showFieldErrors } from "../components/ui/Field";
import { Button } from "../components/ui/Button";

// Set a first password from an approved account request (M1.3). Adults
// get a full session; teens get the shared waiting-for-consent page.
export function SetPassword() {
  const [params] = useSearchParams();
  const token = params.get("token") ?? "";
  const navigate = useNavigate();
  const { refresh } = useAuth();
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [mismatch, setMismatch] = useState(false);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (password !== confirm) {
      showFieldErrors(e.currentTarget as HTMLFormElement, () => setMismatch(true));
      return;
    }
    setMismatch(false);
    setBusy(true);
    setError(null);
    try {
      const result = await AccountRequests.confirm(token, password);
      setPassword("");
      setConfirm("");
      await refresh();
      navigate(result.pendingConsent ? "/waiting-for-consent" : "/", { replace: true });
    } catch (err) {
      setError(
        errorCode(err) === "link_expired"
          ? "This link has expired or was already used. Ask your church office for a new one."
          : errorText(err, "We couldn't set your password.")
      );
    } finally {
      setBusy(false);
    }
  }

  if (!token) {
    return (
      <div className="fold-auth wrap-anywhere">
        <h1 className="fold-page-header text-3xl font-bold mb-6">Set your password</h1>
        <div className="space-y-4">
          <p className="text-sm">This link is missing its token.</p>
          <p className="mt-4">
            <Link to="/login" className="inline-flex min-h-11 items-center text-primary font-semibold text-sm">Back to log in</Link>
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="fold-auth wrap-anywhere">
      <h1 className="fold-page-header text-3xl font-bold mb-6">Set your password</h1>
      <Form noValidate onSubmit={submit} className="fold-form space-y-5">
        {error && <FormError>{error}</FormError>}
        <Field id="sp-password" label="Password" hint="Must be at least 10 characters, and at most 72 bytes (accents and emoji take more).">
          <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" required />
        </Field>
        <Field id="sp-confirm" label="Confirm password" error={mismatch && "The passwords don't match."}>
          <Input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" required />
        </Field>
        <Button variant="primary" layout="w-full" type="submit" disabled={busy}>
          {busy ? "Saving…" : "Set password"}
        </Button>
      </Form>
    </div>
  );
}
