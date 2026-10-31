import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { useAuth } from "../auth";
import { Messages } from "../messages-api";
import type { ComposeOptions, Message } from "../types";
import { errorText } from "../lib/errors";
import { audienceLabel, statusLabel } from "../lib/messages";
import { reminderAudience, reminderRowTitle } from "../lib/reminders";
import { browserTimeZone, formatWhen } from "../lib/when";
import { useChurchZoneLoad } from "../lib/useEventZone";
import { ErrorNotice } from "../components/ErrorNotice";
import { ButtonLink } from "../components/ui/Button";
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

// GET /messages answers at most this many of each kind, newest first.
const LIST_LIMIT = 200;
export function MessagesPage() {
  const { user, canCompose, isAdmin } = useAuth();
  const church = useChurchZoneLoad();
  const zone = church.zone ?? browserTimeZone();
  const [params, setParams] = useSearchParams();
  const reminders = isAdmin && params.get("show") === "reminders";
  // A leader's list is theirs alone: no category to choose.
  const category = isAdmin ? (reminders ? "reminder" : "announcement") : undefined;
  const [messages, setMessages] = useState<Message[] | null>(null);
  const [options, setOptions] = useState<ComposeOptions | null>(null);
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);

  useEffect(() => {
    let alive = true;
    setMessages(null);
    Promise.all([Messages.list(category), Messages.options()])
      .then(([{ messages: list }, o]) => { if (alive) { setMessages(list); setOptions(o); } })
      .catch((e) => { if (alive) setError(errorText(e, "We couldn't load your messages.")); });
    return () => { alive = false; };
  }, [reload, category]);

  const me = user?.displayName || user?.name;
  const allowed = canCompose || options?.canCompose;
  const newButton = <ButtonLink variant="primary" to="/messages/new" layout="w-full sm:w-auto">New message</ButtonLink>;

  return (
    <div className="max-w-3xl mx-auto px-4 py-6 space-y-5 wrap-anywhere">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <h1 className="text-3xl font-bold">Messages</h1>
        {allowed && messages && (messages.length > 0 || reminders) && newButton}
      </div>
      {isAdmin && (
        <div className="flex flex-wrap gap-2" role="group" aria-label="Show">
          <Chip selected={!reminders} onClick={() => setParams({})}>Announcements</Chip>
          <Chip selected={reminders} onClick={() => setParams({ show: "reminders" })}>Automatic reminders</Chip>
        </div>
      )}
      {error && <ErrorNotice message={error} onRetry={() => { setError(""); setReload((n) => n + 1); }} />}
      {!error && (!messages || !church.done) && <Skeleton kind="lines" count={4} label="Loading your messages…" />}
      {messages && messages.length === 0 && reminders && (
        <EmptyState
          title="No automatic reminders yet"
          body="Event reminders show up here once they go out, with who they reached. Each event's reminder time is in its Edit series form."
        />
      )}
      {messages && messages.length === 0 && !reminders && (allowed ? (
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
      {church.done && messages && messages.length >= LIST_LIMIT && (
        <p className="text-sm text-text-muted">{isAdmin ? `Showing the latest ${LIST_LIMIT} messages.` : `Showing your latest ${LIST_LIMIT} messages.`}</p>
      )}
      {church.done && messages && messages.length > 0 && (
        <ul className="space-y-3">
          {messages.map((m) => (
            <li key={m.id}>
              <Link to={`/messages/${m.id}`} className="block rounded-lg border border-border bg-surface p-4 hover:border-border-strong">
                <span className="block text-sm font-semibold text-text-muted">{statusLabel(m, zone)}</span>
                {m.category === "reminder" ? <>
                  {/* An automatic reminder (M3.6b): its date, not its subject, which holds merge fields. */}
                  <span className="mt-1 block text-lg font-semibold text-text">{reminderRowTitle(m)}</span>
                  <span className="mt-1 block text-sm text-text-muted">To {reminderAudience(m.reminder).toLowerCase()}</span>
                </> : <>
                  <span className="mt-1 block text-lg font-semibold text-text">{m.subject || "Untitled message"}</span>
                  <span className="mt-1 block text-sm text-text-muted">To {audienceLabel(m.audience, options)}</span>
                </>}
                <span className="mt-1 block text-sm text-text-muted">
                  Updated {formatWhen(m.updatedAt)}{m.authorName && m.authorName !== me ? ` · By ${m.authorName}` : ""}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
