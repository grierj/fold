import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Invitations as InvitationsApi } from "../api";
import { useAuth } from "../auth";
import { errorCode, errorText } from "../lib/errors";

const INVITE_EXPIRED = "This invitation has expired or was already used. Ask the person who invited you for a new one.";
import type { Invitation } from "../types";

// Accept an invitation (M1.2, 5.10). The person sets a password and gets an
// active account; no approval step. The token is single use and expires.
export function Invite() {
  const [params] = useSearchParams();
  const token = params.get("token") ?? "";
  const navigate = useNavigate();
  const { refresh } = useAuth();
  const [invitation, setInvitation] = useState<Invitation | null>(null);
  const [site, setSite] = useState("");
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (!token) {
      setLoading(false);
      return;
    }
    InvitationsApi.get(token)
      .then((res) => {
        setInvitation(res.invitation);
        setSite(res.site);
        setName(res.invitation.name);
      })
      .catch((e) =>
        setError(
          errorCode(e) === "not_found" || errorCode(e) === "link_expired"
            ? INVITE_EXPIRED
            : errorText(e, "We couldn't open this invitation.")
        )
      )
      .finally(() => setLoading(false));
  }, [token]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await InvitationsApi.accept({ token, name, password });
      await refresh();
      navigate("/", { replace: true });
    } catch (e) {
      // "That email already has an account" (email_taken) stays: the
      // person holding the invitation can act on it by signing in.
      setError(
        errorCode(e) === "link_expired"
          ? INVITE_EXPIRED
          : errorCode(e) === "email_taken"
            ? "That email already has an account. Sign in instead."
            : errorText(e, "We couldn't create your account.")
      );
    } finally {
      setBusy(false);
    }
  }

  if (loading) {
    return (
      <div className="max-w-md mx-auto px-4 py-12">
        <p className="text-sm text-text-muted">Loading invitation…</p>
      </div>
    );
  }

  if (!token || (!invitation && error)) {
    return (
      <div className="max-w-md mx-auto px-4 py-12">
        <h1 className="text-3xl font-bold mb-6">You're invited</h1>
        <div className="bg-surface rounded-lg border border-border p-6">
          <p className="text-sm">{error ?? "This link is missing its token."}</p>
          <p className="mt-4">
            <Link to="/login" className="text-primary font-semibold text-sm">
              Back to log in
            </Link>
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="max-w-md mx-auto px-4 py-12">
      <h1 className="text-3xl font-bold mb-2">You're invited to {site}</h1>
      {invitation?.groupName && (
        <p className="text-text-muted mb-6">
          to join the group <span className="font-semibold">{invitation.groupName}</span>
        </p>
      )}
      {!invitation?.groupName && <div className="mb-6" />}
      <form onSubmit={submit} className="bg-surface rounded-lg border border-border p-6 space-y-4">
        {error && <div className="text-danger text-sm">{error}</div>}
        <div>
          <label className="label" htmlFor="invite-your-name">Your name</label>
          <input id="invite-your-name"
            className="input"
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={100}
            required
          />
        </div>
        <div>
          <label className="label" htmlFor="invite-email">Email (you'll sign in with it)</label>
          <input id="invite-email" type="email" className="input bg-page" value={invitation?.email ?? ""} readOnly />
        </div>
        <div>
          <label className="label" htmlFor="inv-password">
            Choose a password
          </label>
          <input
            id="inv-password"
            type="password"
            className="input"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="new-password"
            required
          />
          <p className="text-xs text-text-muted mt-1">Must be at least 10 characters.</p>
        </div>
        <button type="submit" disabled={busy} className="btn-primary w-full">
          {busy ? "Creating account…" : "Create my account"}
        </button>
      </form>
    </div>
  );
}
