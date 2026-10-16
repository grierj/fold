import { useEffect, useState } from "react";
import { useLocation, useNavigate, Link } from "react-router-dom";
import { useAuth } from "../auth";
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
      setError(
        e instanceof ApiError && e.status === 401
          ? "Invalid email or password."
          : e instanceof Error
            ? e.message
            : "Login failed."
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="max-w-md mx-auto px-4 py-12">
      <h1 className="text-3xl font-extrabold mb-6">Log in to Fold</h1>
      <form onSubmit={submit} className="bg-white rounded-xl border border-gray-200 p-6 space-y-4">
        {error && <div className="text-red-600 text-sm">{error}</div>}
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
          <Link to="/forgot-password" className="text-brand font-semibold">
            Forgot your password?
          </Link>
        </p>
      </form>
      <p className="text-sm text-gray-500 mt-4 text-center">
        New here?{" "}
        <Link to="/join" className="text-brand font-semibold">
          Request an account
        </Link>{" "}
        — an admin reviews every request.
      </p>
    </div>
  );
}
