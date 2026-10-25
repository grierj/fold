import { useCallback, useEffect, useRef, useState } from "react";
import { Navigate, useLocation, useParams } from "react-router-dom";
import { ApiError } from "../api";
import { Adds } from "../add-api";
import { useAuth } from "../auth";
import type { AddRequest } from "../types";
import { Button, ButtonLink } from "../components/ui/Button";
import { EmptyState } from "../components/ui/EmptyState";
import { FormError } from "../components/ui/Field";
import { errorText } from "../lib/errors";
import { formatWhen } from "../lib/when";

const statuses: Record<AddRequest["status"], string> = { pending: "Not added yet", granted: "Added", denied: "Denied", withdrawn: "Withdrawn", expired: "Expired", ended: "Ended" };
const decisions = { pending: "Waiting", approved: "Approved", denied: "Denied", not_needed: "Not needed" };
const problems = {
  capacity: "Approval was recorded, but there is no room. Retry when a place is available. Retrying does not allow adding over the original limit.",
  childcare: "Approval was recorded, but childcare is full. Retry when childcare has room.",
  options: "Approval was recorded, but the original attendance options are no longer available. Withdraw this ask and send a new one with suitable options.",
  throttled: "Approval was recorded, but several date additions were made recently. Wait a minute, then retry.",
};

function Preview({ request: r }: { request: AddRequest }) {
  // Date previews are title/time only. Never construct a parent-event link or
  // recover names/context from a selection, notice or previously loaded page.
  if (r.purpose === "occurrence_add") return <div className="space-y-1"><p className="font-semibold">{r.eventTitle || "One event date"}</p>{r.startsAt && <p>{formatWhen(r.startsAt)}{r.endsAt ? ` – ${formatWhen(r.endsAt)}` : ""}</p>}<p className="text-sm text-text-muted">One date only. This preview does not give access to the event, its group or any other dates.</p></div>;
  return <div className="space-y-1"><p className="font-semibold">{r.groupName || "A group membership"}</p>{r.requesterName && r.subjectName && <p>{r.requesterName} → {r.subjectName}</p>}{r.contextId !== undefined && r.groupName && <ButtonLink variant="link" to={`/groups/${r.contextId}`}>View group</ButtonLink>}{!r.groupName && <p className="text-sm text-text-muted">Group details are not available in this preview. Reviewing an ask does not give group access.</p>}</div>;
}

export function AddRequestsPage() {
  const { user } = useAuth();
  const location = useLocation();
  const { id } = useParams();
  if (!user) return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  return <div className="max-w-2xl mx-auto px-4 py-6 space-y-5">
    <h1 className="text-2xl font-bold">Add requests</h1>
    <p className="text-text-muted">Review asks to add someone to a group or one event date. These do not share contact details.</p>
    {id ? <AddRequestDetail key={`${user.id}:${id}`} id={Number(id)} /> : <AddRequestList key={user.id} />}
  </div>;
}

function AddRequestList() {
  const [requests, setRequests] = useState<AddRequest[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [more, setMore] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const generation = useRef(0);
  const refresh = useCallback(() => { generation.current++; setRevision((n) => n + 1); }, []);
  useEffect(() => {
    const current = ++generation.current;
    setRequests([]); setMore(false); setLoaded(false); setError(""); setBusy(false);
    Adds.requests().then(({ requests: rows }) => {
      if (current !== generation.current) return;
      setRequests(rows); setMore(rows.length === 100); setLoaded(true);
    }).catch((e: unknown) => { if (current === generation.current) setError(errorText(e)); });
    window.addEventListener("focus", refresh);
    return () => { generation.current++; window.removeEventListener("focus", refresh); };
  }, [revision, refresh]);
  async function older() {
    const cursor = requests[requests.length - 1]?.id;
    if (!loaded || !more || busy || cursor === undefined) return;
    const current = generation.current;
    setBusy(true); setError("");
    try {
      const { requests: rows } = await Adds.requests(cursor);
      if (current !== generation.current) return;
      setRequests((old) => [...old, ...rows]); setMore(rows.length === 100);
    } catch (e) { if (current === generation.current) setError(errorText(e)); }
    finally { if (current === generation.current) setBusy(false); }
  }
  return <>
    <Button variant="secondary" onClick={refresh}>Refresh</Button>
    {error && <FormError>{error}</FormError>}
    {!loaded && !error && <p role="status">Loading add requests…</p>}
    {loaded && !requests.length && <EmptyState title="No add requests yet" body="Asks you send or can review will appear here. To send an ask, select a person by name in a group or event date you manage." />}
    <ul className="space-y-3">{requests.map((r) => <li key={r.id} className="border border-border rounded-lg bg-surface p-4 space-y-2 wrap-anywhere">
      <Preview request={r} /><p>{statuses[r.status]}</p>
      {r.needsAdmin && <p className="text-sm">Teen: {decisions[r.subjectDecision]} · Admin: {decisions[r.adminDecision]}</p>}
      <ButtonLink variant="link" to={`/add-requests/${r.id}`}>Review ask #{r.id}</ButtonLink>
    </li>)}</ul>
    {loaded && more && requests.length > 0 && <Button variant="secondary" disabled={busy} onClick={() => void older()}>Load older asks</Button>}
  </>;
}

function AddRequestDetail({ id }: { id: number }) {
  const [request, setRequest] = useState<AddRequest | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [revision, setRevision] = useState(0);
  const generation = useRef(0);
  useEffect(() => {
    const current = ++generation.current;
    setLoaded(false); setRequest(null); setError(""); setBusy(false);
    async function load() {
      if (!Number.isSafeInteger(id) || id <= 0) { if (generation.current === current) setLoaded(true); return; }
      try {
        const { request: r } = await Adds.request(id);
        if (generation.current === current) { setRequest(r); setLoaded(true); }
      } catch (e) {
        if (generation.current !== current) return;
        if (e instanceof ApiError && (e.status === 404 || e.status === 403)) setLoaded(true);
        else setError(errorText(e));
      }
    }
    void load();
    const refresh = () => setRevision((n) => n + 1);
    window.addEventListener("focus", refresh);
    return () => { generation.current++; window.removeEventListener("focus", refresh); };
  }, [id, revision]);
  async function action(kind: "withdraw" | "complete" | "subject" | "admin", decision?: "approved" | "denied") {
    const current = generation.current;
    setBusy(true); setError("");
    try {
      const result = kind === "withdraw" ? await Adds.withdraw(id) : kind === "complete" ? await Adds.complete(id) : await Adds.decide(id, kind, decision!);
      if (generation.current === current) setRequest(result.request);
    } catch (e) {
      if (generation.current !== current) return;
      if (e instanceof ApiError && (e.status === 403 || e.status === 404)) { setRequest(null); setLoaded(true); }
      else setError(errorText(e));
    } finally { if (generation.current === current) setBusy(false); }
  }
  const pending = request?.status === "pending";
  return <>
    <div className="flex flex-wrap gap-2"><ButtonLink variant="link" to="/add-requests">All add requests</ButtonLink><Button variant="secondary" onClick={() => setRevision((n) => n + 1)}>Refresh</Button></div>
    {error && <FormError>{error}</FormError>}
    {!loaded && !error && <p role="status">Loading ask…</p>}
    {loaded && !request && <EmptyState title="This ask is not available" body="It may have ended or may not be yours to review. Open your add request history to see what is available to you." />}
    {request && <section aria-label="Ask to add review" className="bg-surface border border-border rounded-lg p-4 space-y-4 wrap-anywhere">
      <h2 className="text-xl font-bold">Ask to add #{request.id}</h2>
      <Preview request={request} />
      <p role="status" className="font-semibold">{statuses[request.status]}</p>
      <p className="text-sm text-text-muted">Sent {formatWhen(request.createdAt)} · Expires {formatWhen(request.expiresAt)}</p>
      {request.purpose === "group_add" && request.reason && <div><h3 className="font-semibold">Reason</h3><p className="whitespace-pre-wrap">{request.reason}</p></div>}
      {request.needsAdmin && <p>Both the teen and an admin must approve, even when an admin sent the ask. Either denial ends it.</p>}
      {pending && request.canComplete && <p>{request.completionProblem ? problems[request.completionProblem] : "Approvals are recorded, but they have not been added. Retry when there is room and the original options are available. Retrying keeps the original options and expiry; it does not allow adding over capacity."}</p>}
      {request.status === "granted" && <p>They were added {request.purpose === "group_add" ? "to the group" : "to this date only"}. No contact details were shared.</p>}
      {request.status === "ended" && <p>This ask can no longer be completed. Check with your church office if you need help.</p>}
      <Decision label={request.needsAdmin ? "Teen's decision" : "Person's decision"} value={request.subjectDecision} canDecide={!!pending && request.canSubjectDecide} busy={busy} onDecide={(d) => void action("subject", d)} />
      {request.needsAdmin && <Decision label="Admin's decision" value={request.adminDecision} canDecide={!!pending && request.canAdminDecide} busy={busy} onDecide={(d) => void action("admin", d)} />}
      <div className="flex flex-wrap gap-2">
        {pending && request.canComplete && <Button variant="primary" disabled={busy} onClick={() => void action("complete")}>Retry adding</Button>}
        {pending && request.canWithdraw && <Button variant="secondary" disabled={busy} onClick={() => void action("withdraw")}>Withdraw ask</Button>}
      </div>
    </section>}
  </>;
}

function Decision({ label, value, canDecide, busy, onDecide }: { label: string; value: AddRequest["adminDecision"]; canDecide: boolean; busy: boolean; onDecide: (d: "approved" | "denied") => void }) {
  return <section aria-label={label} className="border-t border-border pt-3 space-y-2">
    <h3 className="font-semibold">{label}: {decisions[value]}</h3>
    {canDecide && <div className="flex flex-wrap gap-2"><Button variant="primary" disabled={busy} onClick={() => onDecide("approved")}>Approve adding</Button><Button variant="danger-outline" disabled={busy} onClick={() => onDecide("denied")}>Deny ask</Button></div>}
  </section>;
}
