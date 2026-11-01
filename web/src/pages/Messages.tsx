import { useEffect, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { useAuth } from "../auth";
import { Messages } from "../messages-api";
import type { ComposeOptions, Message } from "../types";
import { errorText } from "../lib/errors";
import { audienceLabel, statusLabel } from "../lib/messages";
import { reminderAudience, reminderRowTitle } from "../lib/reminders";
import { browserTimeZone, dayOf, formatDayHeading, formatWhen } from "../lib/when";
import { useChurchZoneLoad } from "../lib/useEventZone";
import { ErrorNotice } from "../components/ErrorNotice";
import { SearchBox } from "../components/ui/SearchBox";
import { Field, Select } from "../components/ui/Field";
import { Button, ButtonLink } from "../components/ui/Button";
import { Chip } from "../components/ui/Chip";
import { EmptyState } from "../components/ui/EmptyState";
import { Skeleton } from "../components/ui/Skeleton";

// The viewer's messages (M3.4c): drafts, scheduled and sent, newest change
// first, each with its state in words, its subject, who it goes to and when
// it last changed. An admin sees everyone's, with the author's name. When a
// message goes out reads on the church's clock; "Updated" on the viewer's.
// An admin also sees automatic event reminders (M3.6b), named by their
// date, under their own chip so they can't crowd announcements out; a
// leader's list has only what they wrote, with no chips.

const LIST_LIMIT = 25;
const STATUSES = ["draft", "scheduled", "sending", "sent", "failed", "cancelled"];
export function MessagesPage() {
  const { user, canCompose, isAdmin } = useAuth();
  const church = useChurchZoneLoad();
  const zone = church.zone ?? browserTimeZone();
  const [params, setParams] = useSearchParams();
  const search = params.get("search") ?? "";
  const status = STATUSES.includes(params.get("status") ?? "") ? params.get("status")! : "";
  const cursor = params.get("cursor") ?? "";
  const [debounced, setDebounced] = useState(search);
  const [nextCursor, setNextCursor] = useState("");
  const [resolvedKey, setResolvedKey] = useState("");
  const count = useRef<HTMLParagraphElement>(null);
  const paging = useRef(false);
  useEffect(() => { const timer = setTimeout(() => setDebounced(search), 300); return () => clearTimeout(timer); }, [search]);
  function change(key: string, value: string) {
    paging.current = false;
    setParams((old) => { const next = new URLSearchParams(old); next.delete("cursor"); if (value) next.set(key, value); else next.delete(key); return next; }, { replace: true });
  }
  function move(cursor: string) {
    if (cursor && !canPage) return;
    paging.current = true;
    setParams((old) => { const next = new URLSearchParams(old); if (cursor) next.set("cursor", cursor); else next.delete("cursor"); return next; });
  }
  const reminders = isAdmin && params.get("show") === "reminders";
  // A leader's list is theirs alone: no category to choose.
  const category = isAdmin ? (reminders ? "reminder" : "announcement") : undefined;
  const requestKey = JSON.stringify([category, debounced, status, cursor]);
  const [messages, setMessages] = useState<Message[] | null>(null);
  const canPage = !!messages && search === debounced && resolvedKey === JSON.stringify([category, search, status, cursor]);
  const [options, setOptions] = useState<ComposeOptions | null>(null);
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);

  useEffect(() => {
    let alive = true;
    setMessages(null); setError("");
    Promise.all([Messages.list(category, { limit: LIST_LIMIT, cursor, search: debounced, status }), Messages.options()])
      .then(([{ messages: list, nextCursor }, o]) => { if (alive) { setMessages(list); setNextCursor(nextCursor ?? ""); setOptions(o); setResolvedKey(requestKey); } })
      .catch((e) => { if (alive) setError(errorText(e, "We couldn't load your messages.")); });
    return () => { alive = false; };
  }, [reload, category, cursor, debounced, status, requestKey]);
  useEffect(() => {
    if (!messages || !paging.current) return;
    paging.current = false;
    count.current?.scrollIntoView?.({ block: "start" });
    count.current?.focus({ preventScroll: true });
  }, [messages]);

  const me = user?.displayName || user?.name;
  const allowed = canCompose || options?.canCompose;
  const newButton = <ButtonLink variant="primary" to="/messages/new" layout="w-full sm:w-auto">New message</ButtonLink>;

  return (
    <div className="fold-page fold-page-narrow space-y-6 wrap-anywhere">
      <div className="fold-page-header">
        <h1 className="text-3xl font-bold">Messages</h1>
        {allowed && messages && (messages.length > 0 || reminders) && newButton}
      </div>
      {isAdmin && (
        <div className="flex flex-wrap gap-2" role="group" aria-label="Show">
          <Chip selected={!reminders} onClick={() => change("show", "")}>Announcements</Chip>
          <Chip selected={reminders} onClick={() => change("show", "reminders")}>Automatic reminders</Chip>
        </div>
      )}
      <div className="flex flex-wrap items-end gap-3">
        <div className="min-w-48 flex-1"><SearchBox label="Search message subjects" value={search} onChange={(v) => change("search", v)} /></div>
        <Field label="Message status"><Select value={status} onChange={(e) => change("status", e.target.value)} options={[{ value: "", label: "All statuses" }, ...STATUSES.map((s) => ({ value: s, label: s[0].toUpperCase() + s.slice(1) }))]} /></Field>
      </div>
      {error && <ErrorNotice message={error} onRetry={() => { setError(""); setReload((n) => n + 1); }} />}
      {!error && (!messages || !church.done) && <Skeleton kind="lines" count={4} label="Loading your messages…" />}
      {messages && messages.length === 0 && (search || status || cursor) && <EmptyState title="No messages match" body="Try another subject, choose another status, or return to the newest messages." />}
      {messages && messages.length === 0 && !search && !status && !cursor && reminders && (
        <EmptyState
          title="No automatic reminders yet"
          body="Event reminders show up here once they go out, with who they reached. Each event's reminder time is in its Edit series form."
        />
      )}
      {messages && messages.length === 0 && !search && !status && !cursor && !reminders && (allowed ? (
        <EmptyState
          title="No messages yet"
          body="Emails you write to your groups and events show up here: drafts, scheduled ones and sent ones."
          action={newButton}
        />
      ) : (
        <EmptyState
          title="Messages are for leaders"
          body="Group leaders, event hosts and admins write to their groups and events here. Ask a leader if you'd like something sent."
          action={<ButtonLink variant="secondary" to="/my-groups">Go to my groups</ButtonLink>}
        />
      ))}
      {messages && <p ref={count} tabIndex={-1} role="status" className="scroll-mt-24 text-sm text-text-muted">{messages.length} {messages.length === 1 ? "message" : "messages"} on this page{nextCursor ? "; older messages available" : ""}</p>}
      {church.done && messages && messages.length > 0 && (
        <ul className="fold-list">
          {messages.map((m, index) => (
            <li key={m.id}>
              {(index === 0 || dayOf(messages[index - 1].updatedAt) !== dayOf(m.updatedAt)) && <h2 className="pt-4 font-semibold">Updated {formatDayHeading(dayOf(m.updatedAt))}</h2>}
              <Link to={`/messages/${m.id}`} className="fold-list-row grid gap-x-6 rounded-sm px-3 hover:bg-sunken sm:grid-cols-[minmax(0,1fr)_auto]">
                <span className="text-sm font-semibold text-text-muted sm:col-start-2 sm:row-start-1 sm:max-w-60 sm:text-right">{statusLabel(m, zone)}</span>
                {m.category === "reminder" ? <>
                  {/* An automatic reminder (M3.6b): its date, not its subject, which holds merge fields. */}
                  <span className="text-lg font-semibold text-text sm:col-start-1 sm:row-start-1">{reminderRowTitle(m)}</span>
                  <span className="mt-1 text-sm text-text-muted sm:col-start-1">To {reminderAudience(m.reminder).toLowerCase()}</span>
                </> : <>
                  <span className="text-lg font-semibold text-text sm:col-start-1 sm:row-start-1">{m.subject || "Untitled message"}</span>
                  <span className="mt-1 text-sm text-text-muted sm:col-start-1">To {audienceLabel(m.audience, options)}</span>
                </>}
                <span className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-sm text-text-muted sm:col-span-2">
                  <span>Updated {formatWhen(m.updatedAt)}</span>
                  {m.authorName && m.authorName !== me && <span>By {m.authorName}</span>}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
      {(cursor || (messages && nextCursor)) && <nav aria-label="Message pages" className="flex flex-wrap gap-3">
        {cursor && <Button variant="secondary" onClick={() => move("")}>Newest messages</Button>}
        {messages && nextCursor && <Button variant="secondary" disabled={!canPage} onClick={() => move(nextCursor)}>Older messages</Button>}
      </nav>}
    </div>
  );
}
