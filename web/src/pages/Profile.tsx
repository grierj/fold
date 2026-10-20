import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { AccountRequests, CalendarFeeds, Profile as ProfileApi } from "../api";
import type { CalendarFeed } from "../types";
import { useAuth } from "../auth";
import { Button } from "../components/ui/Button";
import { AdminIcon } from "../components/icons";
import { ImageUpload } from "../components/ImageUpload";
import { AccountRequestForm } from "../components/AccountRequestForm";
import { ZonePicker } from "../components/ui/ZonePicker";
import { errorText } from "../lib/errors";
import { Choices, Field, FormError, Input, Radio, showFieldErrors } from "../components/ui/Field";
import { Skeleton } from "../components/ui/Skeleton";
import { ErrorNotice } from "../components/ErrorNotice";

export function ProfilePage() {
  const { user, loading, refresh, logout, isAdmin } = useAuth();
  const [displayName, setDisplayName] = useState<string | null>(null);
  const [name, setName] = useState<string | null>(null);
  const [phone, setPhone] = useState<string | null>(null);
  const [zone, setZone] = useState<string | null>(null);
  const [pictureBusy, setPictureBusy] = useState(false);
  const [pictureStatus, setPictureStatus] = useState<string | null>(null);
  const [pictureError, setPictureError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [passwordSaving, setPasswordSaving] = useState(false);
  const [passwordSaved, setPasswordSaved] = useState(false);
  const [passwordError, setPasswordError] = useState<string | null>(null);
  const [passwordFieldError, setPasswordFieldError] = useState<{ field: "new" | "confirm"; message: string } | null>(null);
  const [newEmail, setNewEmail] = useState("");
  const [emailSaving, setEmailSaving] = useState(false);
  const [emailStatus, setEmailStatus] = useState<string | null>(null);
  const [emailError, setEmailError] = useState<string | null>(null);

  if (loading) {
    return (
      <div className="max-w-2xl mx-auto px-4 py-12">
        <Skeleton kind="page" />
      </div>
    );
  }
  if (!user) {
    return (
      <div className="max-w-2xl mx-auto px-4 py-12 text-center">
        <Link to="/login" state={{ from: "/profile" }} className="text-primary font-semibold">
          Log in
        </Link>{" "}
        to view your profile.
      </div>
    );
  }

  const dn = displayName ?? user.displayName;
  const fullName = name ?? user.name;
  const ph = phone ?? user.phone;
  const tz = zone ?? user.timezone;
  const focalX = user.avatarFocalX;
  const focalY = user.avatarFocalY;

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setSaving(true);
    setError(null);
    setSaved(false);
    try {
      await ProfileApi.update({ displayName: dn, name: fullName, phone: ph, ...(tz ? { timezone: tz } : {}) });
      await refresh();
      setSaved(true);
    } catch (err) {
      setError(errorText(err, "We couldn't save your changes."));
    } finally {
      setSaving(false);
    }
  }

  async function savePictureFile(file: File | null) {
    if (!file) return;
    setPictureBusy(true);
    setPictureError(null);
    setPictureStatus("Saving picture…");
    try {
      await ProfileApi.uploadAvatar(file);
      await refresh();
      setPictureStatus("Picture saved.");
    } catch (err) {
      setPictureStatus(null);
      setPictureError(errorText(err, "We couldn't save your picture."));
    } finally {
      setPictureBusy(false);
    }
  }

  async function saveFocal(next: { x: number; y: number }) {
    if (next.x === focalX && next.y === focalY) return;
    setPictureBusy(true);
    setPictureError(null);
    setPictureStatus("Saving picture…");
    try {
      await ProfileApi.update({ avatarFocal: next });
      await refresh();
      setPictureStatus("Picture saved.");
    } catch (err) {
      setPictureStatus(null);
      setPictureError(errorText(err, "We couldn't save your picture."));
    } finally {
      setPictureBusy(false);
    }
  }

  async function changeEmail(e: React.FormEvent) {
    e.preventDefault();
    setEmailError(null);
    setEmailStatus(null);
    setEmailSaving(true);
    try {
      const res = await ProfileApi.update({ email: newEmail });
      if (res.verificationSent) {
        setEmailStatus(`Verification link sent to ${newEmail}. It expires in 24 hours.`);
        setNewEmail("");
      } else {
        await refresh();
        setEmailStatus("Email updated.");
      }
    } catch (err) {
      setEmailError(errorText(err, "We couldn't start the email change."));
    } finally {
      setEmailSaving(false);
    }
  }

  async function changePassword(e: React.FormEvent) {
    e.preventDefault();
    setPasswordError(null);
    setPasswordSaved(false);
    const form = e.currentTarget as HTMLFormElement;
    if (newPassword.length < 10) {
      showFieldErrors(form, () => setPasswordFieldError({ field: "new", message: "Password must be at least 10 characters." }));
      return;
    }
    if (newPassword !== confirmPassword) {
      showFieldErrors(form, () => setPasswordFieldError({ field: "confirm", message: "Passwords do not match." }));
      return;
    }
    setPasswordFieldError(null);
    setPasswordSaving(true);
    try {
      await ProfileApi.changePassword(currentPassword, newPassword);
      setCurrentPassword("");
      setNewPassword("");
      setConfirmPassword("");
      setPasswordSaved(true);
    } catch (err) {
      setPasswordError(errorText(err, "We couldn't change your password."));
    } finally {
      setPasswordSaving(false);
    }
  }

  async function removeAvatar() {
    if (!confirm("Remove your profile picture?")) return;
    setPictureBusy(true);
    setPictureError(null);
    setPictureStatus(null);
    try {
      await ProfileApi.deleteAvatar();
      await refresh();
      setPictureStatus("Picture removed.");
    } catch (err) {
      setPictureError(errorText(err, "We couldn't remove that."));
    } finally {
      setPictureBusy(false);
    }
  }

  return (
    <div className="max-w-xl mx-auto px-4 py-8">
      <h1 className="text-3xl font-bold mb-6">Your profile</h1>
      {isAdmin && (
        // Phones have no Admin link in the header; it's a row here (PR #65 review F2).
        <Link
          to="/admin"
          className="sm:hidden mb-6 flex min-h-11 items-center gap-3 rounded-lg border border-border bg-surface px-4 font-medium"
        >
          <AdminIcon />
          Admin
        </Link>
      )}

      <div className="bg-surface rounded-lg border border-border p-6 mb-6">
        <ImageUpload
          key={user.avatarUrl || "none"}
          label="Profile picture"
          initialUrl={user.avatarUrl || undefined}
          initialFocal={{ x: user.avatarFocalX, y: user.avatarFocalY }}
          maxDim={512}
          aspectClass="aspect-square max-w-48!"
          onFile={savePictureFile}
          onFocal={saveFocal}
        />
        <p className="text-xs text-text-muted mt-3">
          A new picture is saved as soon as you choose it.
        </p>
        {pictureStatus && <div className="text-success text-sm mt-3">{pictureStatus}</div>}
        {pictureError && <FormError layout="mt-3">{pictureError}</FormError>}
        {user.avatarUrl && (
          <Button variant="danger-outline" layout="mt-3" onClick={removeAvatar} disabled={pictureBusy}>
            Remove picture
          </Button>
        )}
      </div>

      <form onSubmit={save} className="space-y-6">
        {error && <FormError>{error}</FormError>}
        {saved && <div className="text-success text-sm">Profile saved.</div>}

        <div className="bg-surface rounded-lg border border-border p-6 space-y-4">
          <Field id="profile-display-name" label="Display name" optional hint="How your name appears to others.">
            <Input
              value={dn}
              onChange={(e) => setDisplayName(e.target.value)}
              maxLength={100}
            />
          </Field>
          <Field id="profile-full-name" label="Full name">
            <Input
              value={fullName}
              onChange={(e) => setName(e.target.value)}
              maxLength={100}
            />
          </Field>
          <Field id="profile-email" label="Email (you sign in with it)" hint="Change it below; the new address gets a verification link first.">
            <Input type="email" value={user.email} readOnly />
          </Field>
          <Field id="profile-phone" label="Phone" optional>
            <Input
              type="tel"
              value={ph}
              onChange={(e) => setPhone(e.target.value)}
              maxLength={30}
            />
          </Field>
          <Field
            id="profile-tz"
            label="Time zone for emails"
            hint="Emails show event times in this zone. On screen, times always follow this device's clock."
          >
            <ZonePicker value={tz} onChange={setZone} />
          </Field>
          <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={saving}>
            {saving ? "Saving…" : "Save profile"}
          </Button>
        </div>
      </form>

      <form onSubmit={changeEmail} className="bg-surface rounded-lg border border-border p-6 space-y-4 mt-6">
        <h2 className="text-xl font-bold">Change sign-in email</h2>
        {emailError && <FormError>{emailError}</FormError>}
        {emailStatus && <div className="text-success text-sm">{emailStatus}</div>}
        <Field id="new-email" label="New email" hint={<>A verification link goes to the new address, and a notice to {user.email}. The change applies after you verify, and your other sessions are signed out.</>}>
          <Input
            type="email"
            value={newEmail}
            onChange={(e) => setNewEmail(e.target.value)}
            autoComplete="email"
            required
          />
        </Field>
        <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={emailSaving}>
          {emailSaving ? "Sending…" : "Send verification link"}
        </Button>
      </form>

      <form onSubmit={changePassword} className="bg-surface rounded-lg border border-border p-6 space-y-4 mt-6">
        <h2 className="text-xl font-bold">Change password</h2>
        {passwordError && <FormError>{passwordError}</FormError>}
        {passwordSaved && <div className="text-success text-sm">Password changed.</div>}
        <Field id="current-password" label="Current password">
          <Input
            type="password"
            value={currentPassword}
            onChange={(e) => setCurrentPassword(e.target.value)}
            autoComplete="current-password"
            required
          />
        </Field>
        <Field
          id="new-password"
          label="New password"
          hint="Must be at least 10 characters."
          error={passwordFieldError?.field === "new" && passwordFieldError.message}
        >
          <Input
            type="password"
            value={newPassword}
            onChange={(e) => setNewPassword(e.target.value)}
            autoComplete="new-password"
            required
          />
        </Field>
        <Field id="confirm-password" label="Confirm new password" error={passwordFieldError?.field === "confirm" && passwordFieldError.message}>
          <Input
            type="password"
            value={confirmPassword}
            onChange={(e) => setConfirmPassword(e.target.value)}
            autoComplete="new-password"
            required
          />
        </Field>
        <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={passwordSaving}>
          {passwordSaving ? "Saving…" : "Change password"}
        </Button>
      </form>

      <CalendarFeedCard />

      {user.accountType !== "teen" && (
        <div className="mt-6">
          <h2 className="text-xl font-bold mb-3">Invite a friend</h2>
          <p className="text-sm text-text-muted mb-3">
            Ask for an account for a friend. An admin reviews it first, and your friend
            only hears from us once it&rsquo;s approved — leaders and admins can also send
            a direct invitation from the Admin page.
          </p>
          <AccountRequestForm
            submitLabel="Ask an admin to invite them"
            subject="friend"
            onSubmit={(data) => AccountRequests.inviteFriend(data)}
          />
        </div>
      )}

      {/* Phones reach Profile from the Me tab, so Log out lives here, at the
          bottom, away from the header (PR #65 review N1). */}
      <Button variant="secondary" layout="sm:hidden mt-8 w-full" onClick={logout}>
        Log out
      </Button>
    </div>
  );
}

export function CalendarFeedCard() {
  const [loaded, setLoaded] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [hasToken, setHasToken] = useState(false);
  const [scope, setScope] = useState<CalendarFeed["scope"]>("mine");
  const [link, setLink] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState("");
  const linkRef = useRef<HTMLInputElement>(null);
  const alive = useRef(true);

  const load = useCallback(() => {
    setLoaded(false);
    setLoadError(null);
    CalendarFeeds.get()
      .then((r) => {
        if (!alive.current) return;
        setHasToken(r.calendarFeed.hasToken);
        setScope(r.calendarFeed.scope);
        setLoaded(true);
      })
      .catch((err) => {
        // A failed load must never look like "no link yet": Create would
        // then quietly replace a link that works (M17-REVIEW M1).
        if (alive.current) setLoadError(errorText(err, "We couldn't load your calendar link."));
      });
  }, []);

  useEffect(() => {
    alive.current = true;
    load();
    return () => {
      alive.current = false;
    };
  }, [load]);

  async function create() {
    setBusy(true);
    setError(null);
    setStatus("");
    try {
      const r = await CalendarFeeds.create(scope);
      setHasToken(true);
      setScope(r.calendarFeed.scope);
      setLink(r.calendarFeed.url ?? null);
    } catch (err) {
      setError(errorText(err, "We couldn't make your calendar link."));
    } finally {
      setBusy(false);
    }
  }

  async function remake() {
    if (!confirm("Make a new calendar link? Your old link will stop working.")) return;
    await create();
  }

  async function turnOff() {
    if (!confirm("Turn off your calendar link? Calendar apps using it will stop updating.")) return;
    setBusy(true);
    setError(null);
    setStatus("");
    try {
      await CalendarFeeds.revoke();
      setHasToken(false);
      setLink(null);
      setStatus("Calendar link turned off.");
    } catch (err) {
      setError(errorText(err, "We couldn't turn off your calendar link."));
    } finally {
      setBusy(false);
    }
  }

  async function chooseScope(next: CalendarFeed["scope"]) {
    const prev = scope;
    setScope(next);
    if (!hasToken) return;
    setBusy(true);
    setError(null);
    setStatus("");
    try {
      await CalendarFeeds.setScope(next);
      setStatus("Feed choice saved.");
    } catch (err) {
      setScope(prev);
      setError(errorText(err, "We couldn't save that choice."));
    } finally {
      setBusy(false);
    }
  }

  async function copyLink() {
    if (!link) return;
    setError(null);
    if (navigator.clipboard) {
      try {
        await navigator.clipboard.writeText(link);
        setStatus("Link copied.");
        return;
      } catch {
        // Fall through to selecting the text by hand.
      }
    }
    linkRef.current?.select();
    setStatus("The link is selected. Copy it from the box above.");
  }

  return (
    <div className="bg-surface rounded-lg border border-border p-6 space-y-4 mt-6">
      <h2 className="text-xl font-bold">Calendar feed</h2>
      <p className="text-sm text-text-muted">
        Put your Fold events in the calendar app you already use. In your calendar
        app, choose Add calendar &rarr; From URL (or Subscribe) and paste this link.
        It keeps itself up to date.
      </p>
      {loadError ? (
        <ErrorNotice message={loadError} onRetry={load} />
      ) : !loaded ? (
        <Skeleton kind="lines" count={2} label="Loading your calendar link…" />
      ) : (
        <>
          {error && <FormError>{error}</FormError>}
          <Choices legend="Events in the feed">
            <Radio
              name="feed-scope"
              label="My events"
              description="Your groups' events, events you host, and the dates you're going or waitlisted for."
              checked={scope === "mine"}
              disabled={busy}
              onChange={() => void chooseScope("mine")}
            />
            <Radio
              name="feed-scope"
              label="Everything I can see"
              description="Every event you could find here in Fold."
              checked={scope === "all"}
              disabled={busy}
              onChange={() => void chooseScope("all")}
            />
          </Choices>
          {link ? (
            <Field
              id="calendar-feed-link"
              label="Your calendar link"
              hint="This link is shown once. Anyone with it can see these events, so keep it to yourself. Making a new link stops the old one."
            >
              <Input ref={linkRef} value={link} readOnly layout="font-mono text-sm" onFocus={(e) => e.currentTarget.select()} />
            </Field>
          ) : hasToken ? (
            <p className="text-sm text-text-muted">
              Your calendar link is on. We can&rsquo;t show it again, so make a new
              one if you need to set up another device.
            </p>
          ) : (
            <p className="text-sm text-text-muted">You don&rsquo;t have a calendar link yet.</p>
          )}
          {hasToken ? (
            <div className="flex flex-wrap gap-2">
              {link && (
                <Button variant="primary" layout="w-full sm:w-auto" onClick={() => void copyLink()}>
                  Copy link
                </Button>
              )}
              <Button variant="secondary" layout="w-full sm:w-auto" onClick={() => void remake()} disabled={busy}>
                {busy ? "Saving…" : "Make a new link"}
              </Button>
              <Button variant="danger-outline" layout="w-full sm:w-auto" onClick={() => void turnOff()} disabled={busy}>
                Turn off calendar link
              </Button>
            </div>
          ) : (
            <Button variant="primary" layout="w-full sm:w-auto" onClick={() => void create()} disabled={busy}>
              {busy ? "Saving…" : "Create calendar link"}
            </Button>
          )}
        </>
      )}
      <p role="status" className="text-success text-sm empty:hidden">{status}</p>
    </div>
  );
}
