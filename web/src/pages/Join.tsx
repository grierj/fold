import { Link } from "react-router-dom";
import { AccountRequests } from "../api/accountRequests";
import { AccountRequestForm } from "../components/AccountRequestForm";
import { ErrorNotice } from "../components/ErrorNotice";
import { Skeleton } from "../components/ui/Skeleton";
import { errorText } from "../lib/errors";
import { churchName, useSite } from "../lib/useSite";

// Self-signup (M1.3, DEC-4): the person asks for an account and an
// admin decides. The answer is the same whether or not the email
// already has an account; when self-signup is off this page says so
// and the server refuses the form too.
export function Join() {
  // A failed read shows no form: signup may be off, and the server refuses it then.
  const { site, error, loading, retry } = useSite();
  const church = churchName(site);

  return (
    <div className="fold-auth wrap-anywhere">
      <h1 className="text-3xl font-bold mb-2">{church ? `Request an account at ${church}` : "Request an account"}</h1>
      <ol className="list-decimal pl-5 mb-4 space-y-1 text-sm text-text-muted">
        <li>Send your request.</li>
        <li>An admin reviews it.</li>
        <li>We email you a link to set your password.</li>
      </ol>
      <p className="text-sm text-text-muted mb-6">
        Already have an account? <Link to="/login" className="text-link">Log in</Link>
      </p>
      {loading ? (
        <Skeleton kind="lines" />
      ) : error || !site ? (
        <ErrorNotice message={errorText(error, "We couldn't check whether this church takes account requests.")} onRetry={retry} />
      ) : site.selfSignup === "off" ? (
        <div className="fold-section text-text-muted">
          Self-signup is turned off for this church. Ask your church office for an
          account, or log in if you already have one.
        </div>
      ) : (
        <AccountRequestForm submitLabel="Request an account" onSubmit={(data) => AccountRequests.create(data)} />
      )}
    </div>
  );
}
