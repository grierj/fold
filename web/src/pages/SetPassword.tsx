import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { AccountRequests, ApiError } from "../api";
import { useAuth } from "../auth";

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
  const [busy, setBusy] = useState(false);
  const [waiting, setWaiting] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (password !== confirm) {
      setError("The passwords don't match.");
      return;
    }
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
        err instanceof ApiError && err.status === 400
          ? "That link is invalid or has expired. Ask your church office for a new one."
          : err instanceof Error
            ? err.message
            : "Something went wrong."
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
        {error && <div className="text-danger text-sm">{error}</div>}
        <div>
          <label className="label" htmlFor="sp-password">Password</label>
          <input id="sp-password" type="password" className="input" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" required />
          <p className="text-xs text-text-muted mt-1">Must be at least 10 characters.</p>
        </div>
        <div>
          <label className="label" htmlFor="sp-confirm">Confirm password</label>
          <input id="sp-confirm" type="password" className="input" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" required />
        </div>
        <button type="submit" disabled={busy} className="btn-primary w-full">
          {busy ? "Saving…" : "Set password"}
        </button>
      </form>
    </div>
  );
}
