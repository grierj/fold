import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { AccountRequests, Site } from "../api";
import { AccountRequestForm } from "../components/AccountRequestForm";

// Self-signup (M1.3, DEC-4): the person asks for an account and an
// admin decides. The answer is the same whether or not the email
// already has an account; when self-signup is off this page says so
// and the server refuses the form too.
export function Join() {
  const [site, setSite] = useState("");
  const [mode, setMode] = useState<"request" | "off">("request");
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    Site.get()
      .then((s) => {
        setSite(s.name);
        setMode(s.selfSignup);
      })
      .catch(() => {})
      .finally(() => setLoaded(true));
  }, []);

  // "Fold" is the default name, not a church's (the header treats it the same way).
  const church = site && site !== "Fold" ? site : "";

  return (
    <div className="max-w-md mx-auto px-4 py-12">
      <h1 className="text-3xl font-bold mb-2">{church ? `Request an account at ${church}` : "Request an account"}</h1>
      <ol className="list-decimal pl-5 mb-4 space-y-1 text-sm text-text-muted">
        <li>Send your request.</li>
        <li>An admin reviews it.</li>
        <li>We email you a link to set your password.</li>
      </ol>
      <p className="text-sm text-text-muted mb-6">
        Already have an account? <Link to="/login" className="text-link">Log in</Link>
      </p>
      {loaded && mode === "off" ? (
        <div className="bg-surface rounded-lg border border-border p-6 text-sm text-text-muted">
          Self-signup is turned off for this church. Ask your church office for an
          account, or log in if you already have one.
        </div>
      ) : (
        <AccountRequestForm submitLabel="Request an account" onSubmit={(data) => AccountRequests.create(data)} />
      )}
    </div>
  );
}
