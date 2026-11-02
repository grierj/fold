import { lazy, Suspense, useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { ChevronLeftIcon } from "../components/icons";
import { useAuth } from "../auth";
import { Messages } from "../messages-api";
import type { MessageRead } from "../types";
import { errorCode, errorText } from "../lib/errors";
import { formatWhen, lowerLead } from "../lib/when";
import { ErrorNotice } from "../components/ErrorNotice";
import { ButtonLink } from "../components/ui/Button";
import { EmptyState } from "../components/ui/EmptyState";
import { Skeleton } from "../components/ui/Skeleton";

// The editor's read-only view draws the body through its own schema, so
// only the composer's formatting can appear (the server sanitized it too).
const RichTextView = lazy(() => import("../components/messages/RichTextEditor").then((m) => ({ default: m.RichTextView })));

// /messages/:id/read (M3.13d2): an announcement as its reader got it, the
// page its push notification opens. The subject, who it is from, when it
// went out and the message, and nothing about who else got it. Anyone who
// didn't get it hears that it isn't here.
export function MessageReadPage() {
  const { id } = useParams<{ id: string }>();
  const [message, setMessage] = useState<MessageRead | null>(null);
  const [failed, setFailed] = useState<{ text: string; missing: boolean } | null>(null);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    let alive = true;
    setFailed(null);
    Messages.read(Number(id))
      .then(({ message: m }) => { if (alive) setMessage(m); })
      .catch((e) => { if (alive) setFailed({ text: errorText(e, "We couldn't open this message."), missing: errorCode(e) === "not_found" }); });
    return () => { alive = false; };
  }, [id, reload]);

  // Back to Messages for those who can open it (authors, admins), else Home.
  const { isAdmin, canCompose } = useAuth();
  const back = isAdmin || canCompose
    ? <ButtonLink variant="link" to="/messages"><ChevronLeftIcon className="h-4 w-4 shrink-0" /><span aria-hidden="true">Messages</span><span className="sr-only">Back to messages</span></ButtonLink>
    : <ButtonLink variant="link" to="/"><ChevronLeftIcon className="h-4 w-4 shrink-0" /><span aria-hidden="true">Home</span><span className="sr-only">Back to home</span></ButtonLink>;
  const page = (children: React.ReactNode) => <div className="fold-page fold-page-narrow space-y-6 wrap-anywhere">{back}{children}</div>;
  if (failed?.missing) {
    return page(
      <EmptyState level={1} title="This message isn't here"
        body="It may have been for someone else, or it hasn't gone out yet. Messages sent to you also arrive by email."
        action={<ButtonLink variant="primary" to="/">Go to home</ButtonLink>} />,
    );
  }
  if (failed) {
    return page(<>
      <h1 className="text-3xl font-bold">Message</h1>
      <ErrorNotice message={failed.text} onRetry={() => setReload((n) => n + 1)} />
    </>);
  }
  if (!message) return page(<Skeleton kind="page" label="Loading the message…" />);
  return page(
    <article aria-labelledby="message-subject" className="space-y-4">
      <header className="fold-page-header flex-col items-start gap-2">
        <h1 id="message-subject" className="text-3xl font-bold">{message.subject || "Message"}</h1>
        <p className="text-text-muted">From {message.from}</p>
        {message.sentAt && <p className="text-sm text-text-muted">Sent {lowerLead(formatWhen(message.sentAt, undefined, new Date(), true))}</p>}
      </header>
      <div className="py-2">
        <Suspense fallback={<Skeleton kind="lines" label="Loading the message…" />}>
          <RichTextView html={message.bodyHtml} />
        </Suspense>
      </div>
    </article>,
  );
}
