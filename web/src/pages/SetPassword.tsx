import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { AccountRequests } from "../api/accountRequests";
import { useAuth } from "../auth";
import { errorCode, errorText } from "../lib/errors";
import { NewPasswordForm } from "../components/NewPasswordForm";

// Set a first password from an approved account request (M1.3). Adults
// get a full session; teens get the shared waiting-for-consent page.
export function SetPassword() {
  const [params] = useSearchParams();
  const token = params.get("token") ?? "";
  const navigate = useNavigate();
  const { signedIn } = useAuth();

  if (!token) {
    return (
      <div className="fold-auth wrap-anywhere">
        <h1 className="fold-page-header text-3xl font-bold mb-6">Set your password</h1>
        <div className="space-y-4">
          <p className="text-sm">This link is missing its token.</p>
          <p className="mt-4">
            <Link to="/login" className="inline-flex min-h-11 items-center text-primary font-semibold text-sm">Back to log in</Link>
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="fold-auth wrap-anywhere">
      <h1 className="fold-page-header text-3xl font-bold mb-6">Set your password</h1>
      <NewPasswordForm
        idPrefix="sp"
        passwordLabel="Password"
        confirmLabel="Confirm password"
        submitLabel="Set password"
        submit={async (password) => {
          const result = await AccountRequests.confirm(token, password);
          navigate(await signedIn(result), { replace: true });
        }}
        describe={(err) => errorCode(err) === "link_expired"
          ? "This link has expired or was already used. Ask your church office for a new one."
          : errorText(err, "We couldn't set your password.")}
      />
    </div>
  );
}
