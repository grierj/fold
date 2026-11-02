import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Guardians } from "../guardian-api";
import type { GuardianEmailPreview } from "../types";
import { Button } from "../components/ui/Button";
import { FormError } from "../components/ui/Field";
import { formatWhen } from "../lib/when";

type Review = { kind: "loading" } | { kind: "ready"; preview: GuardianEmailPreview } | { kind: "done" } | { kind: "expired" };

export function GuardianConsentPage() {
  const [params] = useSearchParams();
  const token = params.get("token") ?? "";
  const [review, setReview] = useState<Review>({ kind: "loading" });
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let active = true;
    setReview({ kind: "loading" });
    Guardians.preview(token).then((preview) => { if (active) setReview({ kind: "ready", preview }); })
      .catch(() => { if (active) setReview({ kind: "expired" }); });
    return () => { active = false; };
  }, [token]);

  async function grant() {
    setBusy(true);
    try { await Guardians.useEmail(token); setReview({ kind: "done" }); }
    catch { setReview({ kind: "expired" }); }
    finally { setBusy(false); }
  }

  return <div className="fold-auth">
    <h1 className="text-3xl font-bold mb-6">Review account consent</h1>
    <div className="fold-section space-y-4">
      {review.kind === "loading" && <p>Checking your link…</p>}
      {review.kind === "ready" && <>
        <p>Give account consent for <strong>{review.preview.teenName}</strong>?</p>
        <p className="text-sm text-text-muted">This lets your teen sign in and use Fold. It does not give SMS or photo consent. You can revoke consent from your profile at any time.</p>
        <p className="text-sm text-text-muted">Opening this page has not given consent.</p>
        <p className="text-sm text-text-muted">Link expires: {formatWhen(review.preview.expiresAt)}.</p>
        <Button variant="primary" layout="w-full sm:w-auto" disabled={busy} onClick={grant}>{busy ? "Saving…" : "Grant account consent"}</Button>
      </>}
      {review.kind === "done" && <p role="status">Account consent granted. Your teen can now sign in unless the church has separately disabled their account.</p>}
      {review.kind === "expired" && <FormError>This link has expired or can no longer be used. Sign in to your profile to review your guardian links and request a fresh consent email.</FormError>}
      <Link to="/profile" className="inline-block text-primary font-semibold py-3">Manage consent in your profile</Link>
    </div>
  </div>;
}
