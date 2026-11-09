import { useEffect, useState } from "react";
import { Navigate, useLocation, useParams } from "react-router-dom";
import { ApiError } from "../api/core";
import { useAuth } from "../auth";
import { Contacts } from "../api/contacts";
import type { ContactGrant, ContactRequest } from "../types";
import { ContactContextLink, ContactGrantCard, contactFieldsLabel } from "../components/ContactGrantCard";
import { Button, ButtonLink } from "../components/ui/Button";
import { EmptyState } from "../components/ui/EmptyState";
import { FormError } from "../components/ui/Field";
import { Decision, decisionLabels } from "../components/Decision";
import { errorText } from "../lib/errors";
import { useCursorList } from "../lib/useCursorList";
import { formatWhen } from "../lib/when";

const statusLabels: Record<ContactRequest["status"], string> = {
  pending: "Waiting for a decision", granted: "Approved", denied: "Denied", withdrawn: "Withdrawn", expired: "Expired", ended: "Ended",
};

export function ContactRequestsPage() {
  const { user } = useAuth();
  const location = useLocation();
  const { id } = useParams();
  if (!user) return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  return <div className="fold-page-narrow space-y-5">
    <h1 className="text-2xl font-bold">Direct contact</h1>
    <p className="text-text-muted">Review requests and manage shared contact access. Contact details stay private.</p>
    {id ? <ContactRequestDetail key={`${user.id}:${id}`} id={Number(id)} /> : <ContactRequestLists key={user.id} />}
  </div>;
}

function ContactRequestLists() {
  const requests = useCursorList(async (before) => (await Contacts.requests(before)).requests);
  const grants = useCursorList(async (before) => (await Contacts.grants(before)).grants);
  const loaded = requests.loaded && grants.loaded;
  const error = requests.error || grants.error;
  const busy = requests.busy || grants.busy;
  const refresh = () => { requests.refresh(); grants.refresh(); };
  return <>
    {error && <><FormError>{error}</FormError><Button variant="secondary" onClick={refresh}>Try again</Button></>}
    {!loaded && !error && <p role="status">Loading your contact requests…</p>}
    {loaded && <>
      <section aria-label="Request history" className="space-y-3"><h2 className="text-xl font-bold">Request history</h2>
        {!requests.items.length && <EmptyState title="No contact requests yet" body="Requests you send or can review will appear here. To ask for contact details, open the person's card in a group or event you manage." />}
        <ul className="fold-list">{requests.items.map((r) => <li key={r.id} className="fold-list-row wrap-anywhere">
          <p className="font-semibold">{r.requesterName} → {r.subjectName}</p>
          <p>{contactFieldsLabel(r.fields)} · {statusLabels[r.status]}</p>
          {r.needsAdmin && <p className="text-sm">Person: {decisionLabels[r.subjectDecision]} · Admin: {decisionLabels[r.adminDecision]}</p>}
          <ButtonLink variant="link" to={`/contact-requests/${r.id}`}>Review request #{r.id}</ButtonLink>
        </li>)}</ul>
        {requests.more && <Button variant="secondary" disabled={busy} onClick={() => void requests.older()}>Load older requests</Button>}
      </section>
      <section aria-label="Contact grants" className="space-y-3"><h2 className="text-xl font-bold">Contact grants</h2>
        <p className="text-sm text-text-muted">Guardians see only active teen grants here, not pending requests or ended grant history.</p>
        {!grants.items.length && <EmptyState title="No contact grants" body="Approved contact access you can manage will appear here. Review a request above when a decision is needed." />}
        {grants.items.map((g) => <ContactGrantCard key={`${grants.revision}:${g.id}`} grant={g} revealVariant="secondary" onChanged={refresh} />)}
        {grants.more && <Button variant="secondary" disabled={busy} onClick={() => void grants.older()}>Load older grants</Button>}
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
    {loaded && request && <section aria-label="Contact request review" className="fold-section space-y-3 wrap-anywhere">
      <h2 className="text-xl font-bold">{request.requesterName} → {request.subjectName}</h2>
      <p>{contactFieldsLabel(request.fields)} · {statusLabels[request.status]}</p>
      <ContactContextLink {...request} />
      <p className="text-sm text-text-muted">Sent {formatWhen(request.createdAt)} · Expires {formatWhen(request.expiresAt)}</p>
      {request.reason ? <div><h3 className="font-semibold">Reason</h3><p className="whitespace-pre-wrap">{request.reason}</p></div> : <p>No reason was added.</p>}
      {request.status === "ended" && <p>The account, management role or context is no longer eligible for this request.</p>}
      {request.needsAdmin && <p>Both the teen and an admin must approve. Either denial ends this request.</p>}
      <div className="space-y-3">
        <Decision label={request.needsAdmin ? "Teen's decision" : "Person's decision"} value={request.subjectDecision} approveLabel="Approve sharing" denyLabel="Deny request" at={request.subjectDecidedAt} canDecide={request.canSubjectDecide && request.status === "pending"} busy={busy} onDecide={(d) => void action("subject", d)} />
        {request.needsAdmin && <Decision label="Admin's decision" value={request.adminDecision} approveLabel="Approve sharing" denyLabel="Deny request" at={request.adminDecidedAt} canDecide={request.canAdminDecide && request.status === "pending"} busy={busy} onDecide={(d) => void action("admin", d)} />}
      </div>
      {request.canWithdraw && request.status === "pending" && <Button variant="secondary" disabled={busy} onClick={() => void action()}>Withdraw request</Button>}
    </section>}
    {loaded && !request && !!grants.length && <p>You can manage this active grant as a guardian. The request reason and decisions are private to its reviewers.</p>}
    {loaded && grants.map((g) => <ContactGrantCard key={`${revision}:${g.id}`} grant={g} onChanged={() => setRevision((n) => n + 1)} />)}
  </>;
}
