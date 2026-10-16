import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Profile as ProfileApi, ApiError } from "../api";
import { useAuth } from "../auth";

// Redeem an email-change verification link (M1.2, 10.1). The token is the
// credential; the change applies and every other session ends.
export function VerifyEmail() {
  const [params] = useSearchParams();
  const token = params.get("token") ?? "";
  const { refresh } = useAuth();
  const [state, setState] = useState<"working" | "done" | "error">("working");

  useEffect(() => {
    if (!token) {
      setState("error");
      return;
    }
    ProfileApi.verifyEmail(token)
      .then(() => refresh())
      .then(() => setState("done"))
      .catch((e: unknown) => {
        if (!(e instanceof ApiError && e.status === 400)) {
          // fall through to the generic error screen
        }
        setState("error");
      });
  }, [token, refresh]);

  return (
    <div className="max-w-md mx-auto px-4 py-12">
      <h1 className="text-3xl font-bold mb-6">Verify your email</h1>
      <div className="bg-surface rounded-lg border border-border p-6">
        {state === "working" && <p className="text-sm text-text-muted">Verifying…</p>}
        {state === "done" && (
          <>
            <p className="text-sm">Your sign-in email is updated. Your other sessions were signed out.</p>
            <p className="mt-4">
              <Link to="/profile" className="text-primary font-semibold text-sm">
                View your profile
              </Link>
            </p>
          </>
        )}
        {state === "error" && (
          <>
            <p className="text-sm">This link has expired or was already used. Start the email change again from your profile.</p>
            <p className="mt-4 text-sm text-text-muted">
              Change your email again from your{" "}
              <Link to="/profile" className="text-primary font-semibold">
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
