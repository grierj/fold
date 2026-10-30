import { useEffect, useId, useRef, useState } from "react";
import { ApiError, Groups, Invitations } from "../api";
import type { PersonSearchResult } from "../types";
import { errorCode, errorText } from "../lib/errors";
import { AddRequestForm } from "./AddRequestForm";
import { Button } from "./ui/Button";
import { Checkbox, Field, FormError, Input } from "./ui/Field";
import { Form } from "./ui/Form";
import { PeoplePicker } from "./ui/PeoplePicker";

// One "Add people" panel on a group's Members tab (DEC-99). It starts with the
// name search (DEC-78's main path, including Ask to add for a teen). "Use an
// email instead" switches to one email field: it tries to add an existing
// account first, and when there is none, offers the invitation for that same
// email. Every call and rule is the one the three old forms used.
export function AddPeople({ groupId, groupName, memberCount, memberLimit, canSearch, onAdded }: {
  groupId: number;
  groupName: string;
  memberCount: number;
  memberLimit: number | null;
  // The name search needs an active adult account (it is not offered to others).
  canSearch: boolean;
  // Refresh the roster without unmounting the panel.
  onAdded: () => Promise<void> | void;
}) {
  const [open, setOpen] = useState(false);
  const [mode, setMode] = useState<"name" | "email">(canSearch ? "name" : "email");
  const [selection, setSelection] = useState<PersonSearchResult | null>(null);
  const [askPerson, setAskPerson] = useState<PersonSearchResult | null>(null);
  const [email, setEmail] = useState("");
  // The email we found no account for; the invitation goes to exactly this address.
  const [inviteFor, setInviteFor] = useState<string | null>(null);
  const [overLimit, setOverLimit] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<string | null>(null);
  const panelId = useId();
  const full = memberLimit !== null && memberCount >= memberLimit;

  // The link that switched modes is gone once the other form shows, so focus
  // would fall to the page; move it to the new field instead.
  const focusField = useRef(false);
  useEffect(() => {
    if (!focusField.current) return;
    focusField.current = false;
    document.getElementById(mode === "name" ? "add-people-name" : "add-people-email")?.focus();
  }, [mode]);

  function switchTo(next: "name" | "email") {
    focusField.current = true;
    setMode(next);
    setSelection(null);
    setAskPerson(null);
    setEmail("");
    setInviteFor(null);
    setError(null);
    setStatus(null);
  }

  // Adding to a full group is an explicit, audited choice (DEC-39).
  async function add(target: string | { selectionId: string }, label: string) {
    setBusy(true);
    setError(null);
    setStatus(null);
    try {
      try {
        await Groups.addMember(groupId, target);
      } catch (err) {
        if (errorCode(err) !== "group_full") throw err;
        if (!confirm(`“${groupName}” is full (${memberCount} of ${memberLimit}). Add ${label} anyway?`)) return;
        await Groups.addMember(groupId, target, true);
      }
      setSelection(null);
      setEmail("");
      setStatus(`Added ${label}.`);
      await onAdded();
    } catch (err) {
      if (typeof target === "string" && err instanceof ApiError && err.status === 404) setInviteFor(target);
      else if (errorCode(err) === "teen_add_approval_required" && typeof target === "string") {
        setError("Select this person by name, then choose Ask to add. An email address cannot be used to send an ask.");
      } else setError(errorText(err, "We couldn't add that person."));
    } finally {
      setBusy(false);
    }
  }

  function addByName(e: React.FormEvent) {
    e.preventDefault();
    if (!selection) return;
    if (selection.teen) { setAskPerson(selection); return; }
    void add({ selectionId: selection.selectionId }, selection.displayName);
  }

  function addByEmail(e: React.FormEvent) {
    e.preventDefault();
    const address = email.trim();
    if (!address) return;
    setInviteFor(null);
    void add(address, address);
  }

  // Invite them to Fold and into this group (M1.2, 5.10). The person sets a
  // password on acceptance; the server keeps its limits and refusals.
  async function invite() {
    if (!inviteFor) return;
    setBusy(true);
    setError(null);
    setStatus(null);
    try {
      await Invitations.create({ email: inviteFor, groupId, overLimit });
      setStatus(`Invitation sent to ${inviteFor}.`);
      setInviteFor(null);
      setEmail("");
      setOverLimit(false);
    } catch (err) {
      setError(errorText(err, "We couldn't send the invitation."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mt-4">
      <Button variant="secondary" aria-expanded={open} aria-controls={panelId} onClick={() => setOpen(!open)}>
        Add people
      </Button>
      {open && (
        <section id={panelId} aria-label="Add people" className="mt-3 space-y-3 rounded-lg border border-border p-4">
          {error && <FormError>{error}</FormError>}
          {/* Always in the page, so a screen reader announces the text when it changes. */}
          <p role="status" className="text-sm text-success empty:-mb-3">{status}</p>
          {mode === "name" ? (
            <>
              <Form noValidate onSubmit={addByName} className="space-y-3">
                <Field id="add-people-name" label="Find a person by name">
                  <PeoplePicker value={selection} disabled={busy}
                    onChange={(person) => { setSelection(person); setAskPerson(null); setError(null); setStatus(null); }} />
                </Field>
                <Button variant="secondary" type="submit" disabled={busy || !selection}>
                  {busy ? "Adding…" : selection?.teen ? "Ask to add" : "Add member"}
                </Button>
              </Form>
              {askPerson && (
                <AddRequestForm key={askPerson.selectionId} person={askPerson} scope={{ contextType: "group", contextId: groupId }}
                  full={full} onClose={() => setAskPerson(null)} />
              )}
              <Button variant="link" onClick={() => switchTo("email")}>Use an email instead</Button>
            </>
          ) : (
            <>
              <Form noValidate onSubmit={addByEmail} className="space-y-3">
                <Field id="add-people-email" label="Email address">
                  <Input type="email" value={email} placeholder="name@example.com" autoComplete="off"
                    onChange={(e) => { setEmail(e.target.value); setInviteFor(null); setError(null); setStatus(null); }} />
                </Field>
                <Button variant="secondary" type="submit" disabled={busy || !email.trim()}>
                  {busy && !inviteFor ? "Adding…" : "Add member"}
                </Button>
              </Form>
              {inviteFor && (
                <div className="space-y-3 rounded-lg border border-border bg-sunken p-3">
                  <p role="status" className="text-sm">No account uses that email. Invite them to Fold?</p>
                  {full && (
                    <Checkbox label="Invite over the limit anyway" description="This group is full. Going over the limit is logged."
                      checked={overLimit} onChange={(e) => setOverLimit(e.target.checked)} />
                  )}
                  <Button variant="secondary" disabled={busy} onClick={() => void invite()}>
                    {busy ? "Sending…" : "Send invitation"}
                  </Button>
                </div>
              )}
              {canSearch && <Button variant="link" onClick={() => switchTo("name")}>Search by name instead</Button>}
            </>
          )}
        </section>
      )}
    </div>
  );
}
