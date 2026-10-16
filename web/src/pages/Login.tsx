import { useEffect, useState } from "react";
import { useLocation, useNavigate, Link } from "react-router-dom";
import { useAuth } from "../auth";
import { ERROR_COPY, errorText } from "../lib/errors";
import { ApiError } from "../api";

export function Login() {
  const { login, user } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const from = (location.state as { from?: string } | null)?.from ?? "/";

  useEffect(() => {
    if (user) navigate(from, { replace: true });
  }, [user, navigate, from]);

  if (user) {
    return null;
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await login(email, password);
      navigate(from, { replace: true });
    } catch (e) {
      // A 401 here is a wrong email or password (the API says
      // bad_credentials); a 429 is the sign-in lockout, which stays visible.
      setError(e instanceof ApiError && e.status === 401 ? ERROR_COPY.bad_credentials : errorText(e, "We couldn't sign you in."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="max-w-md mx-auto px-4 py-12">
      <h1 className="text-3xl font-bold mb-6">Log in to Fold</h1>
      <form onSubmit={submit} className="bg-surface rounded-lg border border-border p-6 space-y-4">
        {error && <div className="text-danger text-sm">{error}</div>}
        <div>
          <label className="label" htmlFor="email">
            Email
          </label>
          <input
            id="email"
            type="email"
            className="input"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            autoComplete="email"
            required
          />
        </div>
        <div>
          <label className="label" htmlFor="password">
            Password
          </label>
          <input
            id="password"
            type="password"
            className="input"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
            required
          />
        </div>
        <button type="submit" disabled={busy} className="btn-primary w-full">
          {busy ? "Logging in…" : "Log in"}
        </button>
        <p className="text-sm text-center">
          <Link to="/forgot-password" className="inline-flex items-center min-h-11 text-primary font-semibold">
            Forgot your password?
          </Link>
        </p>
      </form>
      <p className="text-sm text-text-muted mt-4 text-center">
        New here?{" "}
        <Link to="/join" className="text-primary font-semibold">
          Request an account
        </Link>{" "}
        — an admin reviews every request.
      </p>
    </div>
  );
}
