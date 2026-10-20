import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { AccountRequests } from "../api";
import { useAuth } from "../auth";
import { errorCode, errorText } from "../lib/errors";
import { Field, FormError, Input, showFieldErrors } from "../components/ui/Field";
import { Button } from "../components/ui/Button";

// Set a first password from an approved account request (M1.3). Adults
// sign in at once; teens stay waiting for consent (M1.10) and see that
// page's message instead of being signed in.
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
  const [waiting, setWaiting] = useState(false);

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
      const res = await AccountRequests.confirm(token, password);
      if (res.pendingConsent) {
        setWaiting(true);
        return;
      }
      await refresh();
      navigate("/", { replace: true });
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
      <div className="max-w-md mx-auto px-4 py-12">
        <h1 className="text-3xl font-bold mb-6">Set your password</h1>
        <div className="bg-surface rounded-lg border border-border p-6">
          <p className="text-sm">This link is missing its token.</p>
          <p className="mt-4">
            <Link to="/login" className="text-primary font-semibold text-sm">Back to log in</Link>
          </p>
        </div>
      </div>
    );
  }

  if (waiting) {
    return (
      <div className="max-w-md mx-auto px-4 py-12">
        <h1 className="text-3xl font-bold mb-6">You&rsquo;re almost in</h1>
        <div className="bg-surface rounded-lg border border-border p-6 text-sm text-text-muted">
          Your password is set. Because you&rsquo;re 13 to 17, your account still needs a
          parent or guardian&rsquo;s consent before you can sign in — your church will be
          in touch when it&rsquo;s ready.
        </div>
      </div>
    );
  }

  return (
    <div className="max-w-md mx-auto px-4 py-12">
      <h1 className="text-3xl font-bold mb-6">Set your password</h1>
      <form onSubmit={submit} className="bg-surface rounded-lg border border-border p-6 space-y-4">
        {error && <FormError>{error}</FormError>}
        <Field id="sp-password" label="Password" hint="Must be at least 10 characters.">
          <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" required />
        </Field>
        <Field id="sp-confirm" label="Confirm password" error={mismatch && "The passwords don't match."}>
          <Input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" required />
        </Field>
        <Button variant="primary" layout="w-full" type="submit" disabled={busy}>
          {busy ? "Saving…" : "Set password"}
        </Button>
      </form>
    </div>
  );
}
