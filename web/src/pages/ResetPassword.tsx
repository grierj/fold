import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Auth as AuthApi } from "../api";
import { useAuth } from "../auth";
import { errorCode, errorText } from "../lib/errors";

// Set a new password from a reset link (M1.2). The token is single use and
// expires; confirming signs the person in.
export function ResetPassword() {
  const [params] = useSearchParams();
  const token = params.get("token") ?? "";
  const navigate = useNavigate();
  const { refresh } = useAuth();
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (password !== confirm) {
      setError("The passwords don't match.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await AuthApi.confirmPasswordReset(token, password);
      await refresh();
      navigate("/", { replace: true });
    } catch (e) {
      setError(
        errorCode(e) === "link_expired"
          ? "This link has expired or was already used. Request a new one below."
          : errorText(e, "We couldn't reset your password.")
      );
    } finally {
      setBusy(false);
    }
  }

  if (!token) {
    return (
      <div className="max-w-md mx-auto px-4 py-12">
        <h1 className="text-3xl font-bold mb-6">Reset your password</h1>
        <div className="bg-surface rounded-lg border border-border p-6">
          <p className="text-sm">This link is missing its token.</p>
          <p className="mt-4">
            <Link to="/forgot-password" className="text-primary font-semibold text-sm">
              Request a new reset link
            </Link>
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="max-w-md mx-auto px-4 py-12">
      <h1 className="text-3xl font-bold mb-6">Choose a new password</h1>
      <form onSubmit={submit} className="bg-surface rounded-lg border border-border p-6 space-y-4">
        {error && <div className="text-danger text-sm">{error}</div>}
        <div>
          <label className="label" htmlFor="rp-password">
            New password
          </label>
          <input
            id="rp-password"
            type="password"
            className="input"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="new-password"
            required
          />
          <p className="text-xs text-text-muted mt-1">Must be at least 10 characters.</p>
        </div>
        <div>
          <label className="label" htmlFor="rp-confirm">
            Confirm new password
          </label>
          <input
            id="rp-confirm"
            type="password"
            className="input"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            autoComplete="new-password"
            required
          />
        </div>
        <button type="submit" disabled={busy} className="btn-primary w-full">
          {busy ? "Saving…" : "Set new password"}
        </button>
      </form>
    </div>
  );
}
