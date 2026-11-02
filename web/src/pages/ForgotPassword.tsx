import { Form } from "../components/ui/Form";
import { useState } from "react";
import { Link } from "react-router-dom";
import { Auth as AuthApi } from "../api";
import { errorText } from "../lib/errors";
import { Field, FormError, Input } from "../components/ui/Field";
import { Button } from "../components/ui/Button";

// Password reset request (M1.2). The server answers uniformly for known and
// unknown addresses, so the message is shown as-is.
export function ForgotPassword() {
  const [email, setEmail] = useState("");
  const [message, setMessage] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const res = await AuthApi.requestPasswordReset(email);
      setMessage(res.message);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="fold-auth wrap-anywhere">
      <h1 className="fold-page-header text-3xl font-bold mb-6">Reset your password</h1>
      {message ? (
        <div className="space-y-4">
          <p className="text-sm">{message}</p>
          <p className="mt-4">
            <Link to="/login" className="inline-flex min-h-11 items-center text-primary font-semibold text-sm">
              Back to log in
            </Link>
          </p>
        </div>
      ) : (
        <Form noValidate onSubmit={submit} className="fold-form space-y-5">
          {error && <FormError>{error}</FormError>}
          <p className="text-sm text-text-muted">
            Enter the email you sign in with and we'll send you a reset link.
          </p>
          <Field id="fp-email" label="Email">
            <Input
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              autoComplete="email"
              required
            />
          </Field>
          <Button variant="primary" layout="w-full" type="submit" disabled={busy}>
            {busy ? "Sending…" : "Send reset link"}
          </Button>
        </Form>
      )}
    </div>
  );
}
