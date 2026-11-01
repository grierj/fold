import { useEffect, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Profile as ProfileApi } from "../api";
import { useAuth } from "../auth";
import { ButtonLink } from "../components/ui/Button";

// Redeem an email-change verification link (M1.2, 10.1). The token is the
// credential; the change applies and every other session ends.
export function VerifyEmail() {
  const [params] = useSearchParams();
  const token = params.get("token") ?? "";
  const { refresh } = useAuth();
  const [state, setState] = useState<"working" | "done" | "error">("working");

  // The token is single-use: redeem it once per token, even when StrictMode
  // runs this effect twice.
  const redeemed = useRef<string | null>(null);

  useEffect(() => {
    if (!token) {
      setState("error");
      return;
    }
    if (redeemed.current === token) return;
    redeemed.current = token;
    ProfileApi.verifyEmail(token)
      .then(() => refresh())
      .then(() => setState("done"))
      .catch(() => setState("error"));
  }, [token, refresh]);

  return (
    <div className="fold-auth">
      <h1 className="text-3xl font-bold mb-6">Verify your email</h1>
      <div className="fold-section">
        {state === "working" && <p className="text-sm text-text-muted">Verifying…</p>}
        {state === "done" && (
          <>
            <p className="text-sm">Your sign-in email is updated. Your other sessions were signed out.</p>
            <p className="mt-4">
              <ButtonLink variant="link" to="/profile">View your profile</ButtonLink>
            </p>
          </>
        )}
        {state === "error" && (
          <>
            <p className="text-sm">This link has expired or was already used. Start the email change again from your profile.</p>
            <p className="mt-4 text-sm text-text-muted">
              Change your email again from your{" "}
              <Link to="/profile" className="inline-flex min-h-11 items-center text-primary font-semibold">
                profile
              </Link>{" "}
              to get a fresh link.
            </p>
          </>
        )}
      </div>
    </div>
  );
}
