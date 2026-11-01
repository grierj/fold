import { Form } from "../components/ui/Form";
import { useEffect, useState } from "react";
import { useLocation, useNavigate, Link } from "react-router-dom";
import { useAuth } from "../auth";
import { ERROR_COPY, errorCode, errorText } from "../lib/errors";
import { ApiError } from "../api";
import { Field, FormError, Input } from "../components/ui/Field";
import { Button, ButtonLink } from "../components/ui/Button";
import { useSiteName } from "../lib/useSiteName";
import { isStandalone } from "../lib/push";

export function Login() {
  const { login, user } = useAuth();
  const site = useSiteName();
  const navigate = useNavigate();
  const location = useLocation();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [waiting, setWaiting] = useState(false);
  const from = (location.state as { from?: string } | null)?.from ?? "/";
  // The Home Screen app keeps its own sign-in (signed_out_app, research 2.7).
  const [homeScreen] = useState(isStandalone);

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
    setWaiting(false);
    try {
      await login(email, password);
      navigate(from, { replace: true });
    } catch (e) {
      // A 401 here is a wrong email or password (the API says
      // bad_credentials); a 429 is the sign-in lockout, which stays visible.
      setWaiting(errorCode(e) === "consent_required");
      setError(e instanceof ApiError && e.status === 401 ? ERROR_COPY.bad_credentials : errorText(e, "We couldn't sign you in."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="max-w-md mx-auto px-4 py-12">
      {/* The church's name, as the header shows it ("Fold" when it has none); plain "Log in" until it loads. */}
      <h1 className="text-3xl font-bold mb-6">{site === undefined ? "Log in" : `Log in to ${site || "Fold"}`}</h1>
      {homeScreen && <p className="-mt-3 mb-6 text-text-muted">Sign in once here and your notifications will come to this app.</p>}
      <Form noValidate onSubmit={submit} className="bg-surface rounded-lg border border-border p-6 space-y-4">
        {error && (waiting
          ? <p role="status" className="rounded-md border border-border bg-sunken p-3 text-text">{error}</p>
          : <FormError>{error}</FormError>)}
        <Field id="email" label="Email">
          <Input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            autoComplete="email"
            required
          />
        </Field>
        <Field id="password" label="Password">
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
            required
          />
        </Field>
        <Button variant="primary" layout="w-full" type="submit" disabled={busy}>
          {busy ? "Logging in…" : "Log in"}
        </Button>
        <p className="text-sm text-center">
          <Link to="/forgot-password" className="inline-flex items-center min-h-11 text-primary font-semibold">
            Forgot your password?
          </Link>
        </p>
      </Form>
      {waiting && <ButtonLink variant="secondary" to="/calendar" layout="mt-4 w-full">Browse public events</ButtonLink>}
      <p className="text-sm text-text-muted mt-4 text-center">
        New here?{" "}
        <Link to="/join" className="text-link">
          Request an account
        </Link>{" "}
        — an admin reviews every request.
      </p>
    </div>
  );
}
