import { lazy, Suspense, useState } from "react";
import { Button } from "./ui/Button";

const ContactRequestForm = lazy(() => import("./ContactRequestForm").then((m) => ({ default: m.ContactRequestForm })));

// Only mount in an authorized management context, for a real account whose
// contact keys were omitted. The server rechecks eligibility at submission.
export function RequestContactButton(props: { subjectUserId: number; subjectName: string; contextType: "group" | "event"; contextId: number }) {
  const [open, setOpen] = useState(false);
  return open ? <Suspense fallback={<p role="status">Loading request form…</p>}><ContactRequestForm {...props} onClose={() => setOpen(false)} /></Suspense>
    : <Button variant="link" onClick={() => setOpen(true)}>Request direct contact</Button>;
}
