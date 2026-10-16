import { useState } from "react";
import { Link } from "react-router-dom";
import { Auth as AuthApi } from "../api";
import { errorText } from "../lib/errors";

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
    <div className="max-w-md mx-auto px-4 py-12">
      <h1 className="text-3xl font-bold mb-6">Reset your password</h1>
      {message ? (
        <div className="bg-surface rounded-lg border border-border p-6">
          <p className="text-sm">{message}</p>
          <p className="mt-4">
            <Link to="/login" className="text-primary font-semibold text-sm">
              Back to log in
            </Link>
          </p>
        </div>
      ) : (
        <form onSubmit={submit} className="bg-surface rounded-lg border border-border p-6 space-y-4">
          {error && <div className="text-danger text-sm">{error}</div>}
          <p className="text-sm text-text-muted">
            Enter the email you sign in with and we'll send you a reset link.
          </p>
          <div>
            <label className="label" htmlFor="fp-email">
              Email
            </label>
            <input
              id="fp-email"
              type="email"
              className="input"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              autoComplete="email"
              required
            />
          </div>
          <button type="submit" disabled={busy} className="btn-primary w-full">
            {busy ? "Sending…" : "Send reset link"}
          </button>
        </form>
      )}
    </div>
  );
}
