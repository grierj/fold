import { useEffect, useState } from "react";
import { Contacts } from "../contact-api";
import type { ContactGrant } from "../types";
import { errorText } from "../lib/errors";
import { ContactGrantCard } from "./ContactGrantCard";
import { Button } from "./ui/Button";
import { EmptyState } from "./ui/EmptyState";
import { FormError } from "./ui/Field";

export function GuardianContactGrants({ teenUserId }: { teenUserId: number }) {
  const [grants, setGrants] = useState<ContactGrant[]>([]);
  const [error, setError] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    let live = true;
    setLoaded(false); setGrants([]); setError("");
    async function load() {
      const rows: ContactGrant[] = [];
      let before: number | undefined;
      for (;;) {
        const page = (await Contacts.grants(before)).grants;
        rows.push(...page.filter((g) => g.subjectUserId === teenUserId && g.needsAdmin && !g.endedAt));
        if (page.length < 100) break;
        const next = page[page.length - 1]?.id;
        if (next === undefined || (before !== undefined && next >= before)) break;
        before = next;
      }
      if (live) { setGrants(rows); setLoaded(true); }
    }
    void load().catch((e: unknown) => { if (live) setError(errorText(e)); });
    const refresh = () => setRevision((n) => n + 1);
    window.addEventListener("focus", refresh);
    return () => { live = false; window.removeEventListener("focus", refresh); };
  }, [teenUserId, revision]);
  return <section aria-label="Active teen contact grants" className="mt-3 space-y-3">
    <h4 className="font-semibold">Active direct contact access</h4>
    <p className="text-sm text-text-muted">You can revoke active access, but only your teen and an admin can approve a request.</p>
    {error && <><FormError>{error}</FormError><Button variant="secondary" onClick={() => setRevision((n) => n + 1)}>Try again</Button></>}
    {!loaded && !error && <p role="status">Loading active grants…</p>}
    {loaded && !grants.length && <EmptyState title="No active contact grants" body="If your teen and an admin approve contact sharing, you can review and revoke that access here." />}
    {grants.map((g) => <ContactGrantCard key={`${revision}:${g.id}`} grant={g} onChanged={() => setRevision((n) => n + 1)} />)}
  </section>;
}
