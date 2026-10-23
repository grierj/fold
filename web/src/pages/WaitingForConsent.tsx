import { useEffect, useRef, useState } from "react";
import { useAuth } from "../auth";
import { Button } from "../components/ui/Button";

export default function WaitingForConsent() {
  const { logout, refresh } = useAuth();
  const heading = useRef<HTMLHeadingElement>(null);
  const [checking, setChecking] = useState(false);
  useEffect(() => heading.current?.focus(), []);
  async function check() {
    setChecking(true);
    try { await refresh(); } finally { setChecking(false); }
  }
  return <main className="max-w-md mx-auto px-4 py-12 space-y-4">
    <p className="text-primary font-bold">Fold</p>
    <h1 ref={heading} tabIndex={-1} className="text-3xl font-bold focus:outline-none">Waiting for consent</h1>
    <p>Your account is ready for a parent or guardian's consent. Your church office can help with the next step.</p>
    <p className="text-text-muted">You can use groups and events after consent is granted.</p>
    <div className="flex flex-col sm:flex-row gap-3">
      <Button variant="primary" disabled={checking} onClick={() => void check()}>{checking ? "Checking…" : "Check account status"}</Button>
      <Button variant="secondary" onClick={() => void logout()}>Log out</Button>
    </div>
    <p role="status" className="text-sm text-text-muted">{checking ? "Checking your account…" : "If consent was just granted, check your account status."}</p>
  </main>;
}
