import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Auth as AuthApi } from "../api/auth";
import { useAuth } from "../auth";
import { errorCode, errorText } from "../lib/errors";
import { NewPasswordForm } from "../components/NewPasswordForm";

// Set a new password from a reset link (M1.2). The token is single use and
// expires; confirming signs the person in.
export function ResetPassword() {
  const [params] = useSearchParams();
  const token = params.get("token") ?? "";
  const navigate = useNavigate();
  const { signedIn } = useAuth();

  if (!token) {
    return (
      <div className="fold-auth wrap-anywhere">
        <h1 className="fold-page-header text-3xl font-bold mb-6">Reset your password</h1>
        <div className="space-y-4">
          <p className="text-sm">This link is missing its token.</p>
          <p className="mt-4">
            <Link to="/forgot-password" className="inline-flex min-h-11 items-center text-primary font-semibold text-sm">
              Request a new reset link
            </Link>
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="fold-auth wrap-anywhere">
      <h1 className="fold-page-header text-3xl font-bold mb-6">Choose a new password</h1>
      <NewPasswordForm
        idPrefix="rp"
        passwordLabel="New password"
        confirmLabel="Confirm new password"
        submitLabel="Set new password"
        submit={async (password) => {
          const result = await AuthApi.confirmPasswordReset(token, password);
          navigate(await signedIn(result), { replace: true });
        }}
        describe={(e) => errorCode(e) === "link_expired"
          ? "This link has expired or was already used. Request a new one below."
          : errorText(e, "We couldn't reset your password.")}
      />
    </div>
  );
}
