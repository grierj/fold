import { useCallback, useEffect, useRef, useState } from "react";
import { Navigate, useLocation, useParams } from "react-router-dom";
import { ApiError } from "../api";
import { useAuth } from "../auth";
import { Contacts } from "../contact-api";
import type { ContactGrant, ContactRequest } from "../types";
import { ContactContextLink, ContactGrantCard, contactFieldsLabel } from "../components/ContactGrantCard";
import { Button, ButtonLink } from "../components/ui/Button";
import { EmptyState } from "../components/ui/EmptyState";
import { FormError } from "../components/ui/Field";
import { errorText } from "../lib/errors";
import { formatWhen } from "../lib/when";

const statusLabels: Record<ContactRequest["status"], string> = {
  pending: "Waiting for a decision", granted: "Approved", denied: "Denied", withdrawn: "Withdrawn", expired: "Expired", ended: "Ended",
};
const decisionLabels = { pending: "Waiting", approved: "Approved", denied: "Denied", not_needed: "Not needed" };

export function ContactRequestsPage() {
  const { user } = useAuth();
  const location = useLocation();
  const { id } = useParams();
  if (!user) return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  return <div className="max-w-2xl mx-auto px-4 py-6 space-y-5">
    <h1 className="text-2xl font-bold">Direct contact</h1>
    <p className="text-text-muted">Review requests and manage shared contact access. Contact details stay private.</p>
    {id ? <ContactRequestDetail key={`${user.id}:${id}`} id={Number(id)} /> : <ContactRequestLists key={user.id} />}
  </div>;
}

function ContactRequestLists() {
  const [requests, setRequests] = useState<ContactRequest[]>([]);
  const [grants, setGrants] = useState<ContactGrant[]>([]);
  const [moreRequests, setMoreRequests] = useState(false);
  const [moreGrants, setMoreGrants] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const generation = useRef(0);
  const refresh = useCallback(() => {
    generation.current++;
    setRevision((n) => n + 1);
  }, []);
  useEffect(() => {
    const current = ++generation.current;
    setLoaded(false); setBusy(false); setError(""); setRequests([]); setGrants([]);
    Promise.all([Contacts.requests(), Contacts.grants()]).then(([r, g]) => {
      if (generation.current !== current) return;
      setRequests(r.requests); setGrants(g.grants); setMoreRequests(r.requests.length === 100); setMoreGrants(g.grants.length === 100); setLoaded(true);
    }).catch((e: unknown) => { if (generation.current === current) setError(errorText(e)); });
    window.addEventListener("focus", refresh);
    return () => { generation.current++; window.removeEventListener("focus", refresh); };
  }, [revision, refresh]);
  async function more(kind: "requests" | "grants") {
    const current = generation.current;
    setBusy(true); setError("");
    try {
      if (kind === "requests") {
        const rows = (await Contacts.requests(requests[requests.length - 1]?.id)).requests;
        if (generation.current !== current) return;
        setRequests((old) => [...old, ...rows]); setMoreRequests(rows.length === 100);
      } else {
        const rows = (await Contacts.grants(grants[grants.length - 1]?.id)).grants;
        if (generation.current !== current) return;
        setGrants((old) => [...old, ...rows]); setMoreGrants(rows.length === 100);
      }
    } catch (e) { if (generation.current === current) setError(errorText(e)); }
    finally { if (generation.current === current) setBusy(false); }
  }
  return <>
    {error && <><FormError>{error}</FormError><Button variant="secondary" onClick={refresh}>Try again</Button></>}
    {!loaded && !error && <p role="status">Loading your contact requests…</p>}
    {loaded && <>
      <section aria-label="Request history" className="space-y-3"><h2 className="text-xl font-bold">Request history</h2>
        {!requests.length && <EmptyState title="No contact requests yet" body="Requests you send or can review will appear here. To ask for contact details, open the person's card in a group or event you manage." />}
        <ul className="space-y-3">{requests.map((r) => <li key={r.id} className="border border-border rounded-lg p-4 bg-surface wrap-anywhere">
          <p className="font-semibold">{r.requesterName} → {r.subjectName}</p>
          <p>{contactFieldsLabel(r.fields)} · {statusLabels[r.status]}</p>
          {r.needsAdmin && <p className="text-sm">Person: {decisionLabels[r.subjectDecision]} · Admin: {decisionLabels[r.adminDecision]}</p>}
          <ButtonLink variant="link" to={`/contact-requests/${r.id}`}>Review request #{r.id}</ButtonLink>
        </li>)}</ul>
        {moreRequests && <Button variant="secondary" disabled={busy} onClick={() => void more("requests")}>Load older requests</Button>}
      </section>
      <section aria-label="Contact grants" className="space-y-3"><h2 className="text-xl font-bold">Contact grants</h2>
        <p className="text-sm text-text-muted">Guardians see only active teen grants here, not pending requests or ended grant history.</p>
        {!grants.length && <EmptyState title="No contact grants" body="Approved contact access you can manage will appear here. Review a request above when a decision is needed." />}
        {grants.map((g) => <ContactGrantCard key={`${revision}:${g.id}`} grant={g} revealVariant="secondary" onChanged={refresh} />)}
        {moreGrants && <Button variant="secondary" disabled={busy} onClick={() => void more("grants")}>Load older grants</Button>}
      </section>
    </>}
  </>;
}

// A guardian's notification uses the request URL, but does not authorize the
// request. Search only the viewer's authorized grants, following exclusive
// cursors; never infer permission to show the request's reason or decisions.
async function matchingGrants(requestId: number): Promise<ContactGrant[]> {
  let before: number | undefined;
  for (;;) {
    const { grants } = await Contacts.grants(before);
    const matches = grants.filter((g) => g.requestId === requestId);
    if (matches.length || grants.length < 100) return matches;
    const next = grants[grants.length - 1]?.id;
    if (next === undefined || (before !== undefined && next >= before)) return [];
    before = next;
  }
}

function ContactRequestDetail({ id }: { id: number }) {
  const [request, setRequest] = useState<ContactRequest | null>(null);
  const [grants, setGrants] = useState<ContactGrant[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    let live = true;
    setLoaded(false); setRequest(null); setGrants([]); setError("");
    async function load() {
      if (!Number.isSafeInteger(id) || id <= 0) throw new ApiError(404, "invalid request");
      let r: ContactRequest | null = null;
      try { r = (await Contacts.request(id)).request; }
      catch (e) { if (!(e instanceof ApiError) || e.status !== 404) throw e; }
      const g = !r || r.status === "granted" ? await matchingGrants(id) : [];
      if (live) { setRequest(r); setGrants(r ? g : g.filter((grant) => !grant.endedAt)); setLoaded(true); }
    }
    void load().catch((e: unknown) => { if (live) setError(errorText(e)); });
    const refresh = () => setRevision((n) => n + 1);
    window.addEventListener("focus", refresh);
    return () => { live = false; window.removeEventListener("focus", refresh); };
  }, [id, revision]);
  async function action(party?: "subject" | "admin", decision?: "approved" | "denied") {
    setBusy(true); setError("");
    try {
      if (party && decision) await Contacts.decide(id, party, decision);
      else await Contacts.withdraw(id);
      setRevision((n) => n + 1);
    } catch (e) { setError(errorText(e)); }
    finally { setBusy(false); }
  }
  return <>
    <ButtonLink variant="link" to="/contact-requests">All contact requests</ButtonLink>
    {error && <><FormError>{error}</FormError><Button variant="secondary" onClick={() => setRevision((n) => n + 1)}>Try again</Button></>}
    {!loaded && !error && <p role="status">Loading request…</p>}
    {loaded && !request && !grants.length && <EmptyState title="Contact access is not available" body="This request or grant may have ended, or may not be yours to review. Open your contact history to see what is available to you." />}
    {loaded && request && <section aria-label="Contact request review" className="bg-surface border border-border rounded-lg p-4 space-y-3 wrap-anywhere">
      <h2 className="text-xl font-bold">{request.requesterName} → {request.subjectName}</h2>
      <p>{contactFieldsLabel(request.fields)} · {statusLabels[request.status]}</p>
      <ContactContextLink {...request} />
      <p className="text-sm text-text-muted">Sent {formatWhen(request.createdAt)} · Expires {formatWhen(request.expiresAt)}</p>
      {request.reason ? <div><h3 className="font-semibold">Reason</h3><p className="whitespace-pre-wrap">{request.reason}</p></div> : <p>No reason was added.</p>}
      {request.status === "ended" && <p>The account, management role or context is no longer eligible for this request.</p>}
      {request.needsAdmin && <p>Both the teen and an admin must approve. Either denial ends this request.</p>}
      <div className="space-y-3">
        <Decision label={request.needsAdmin ? "Teen's decision" : "Person's decision"} value={request.subjectDecision} at={request.subjectDecidedAt} canDecide={request.canSubjectDecide && request.status === "pending"} busy={busy} onDecide={(d) => void action("subject", d)} />
        {request.needsAdmin && <Decision label="Admin's decision" value={request.adminDecision} at={request.adminDecidedAt} canDecide={request.canAdminDecide && request.status === "pending"} busy={busy} onDecide={(d) => void action("admin", d)} />}
      </div>
      {request.canWithdraw && request.status === "pending" && <Button variant="secondary" disabled={busy} onClick={() => void action()}>Withdraw request</Button>}
    </section>}
    {loaded && !request && !!grants.length && <p>You can manage this active grant as a guardian. The request reason and decisions are private to its reviewers.</p>}
    {loaded && grants.map((g) => <ContactGrantCard key={`${revision}:${g.id}`} grant={g} onChanged={() => setRevision((n) => n + 1)} />)}
  </>;
}

function Decision({ label, value, at, canDecide, busy, onDecide }: { label: string; value: ContactRequest["adminDecision"]; at?: string; canDecide: boolean; busy: boolean; onDecide: (d: "approved" | "denied") => void }) {
  return <section aria-label={label} className="border-t border-border pt-3">
    <h3 className="font-semibold">{label}: {decisionLabels[value]}</h3>
    {at && <p className="text-sm text-text-muted">{formatWhen(at)}</p>}
    {canDecide && <div className="flex flex-wrap gap-2 mt-2"><Button variant="primary" disabled={busy} onClick={() => onDecide("approved")}>Approve sharing</Button><Button variant="danger-outline" disabled={busy} onClick={() => onDecide("denied")}>Deny request</Button></div>}
  </section>;
}
