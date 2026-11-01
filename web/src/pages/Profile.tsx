import { lazy, Suspense, useState } from "react";
import { Link, Navigate, NavLink, useLocation, useParams, useSearchParams } from "react-router-dom";
import { AccountRequests, Profile as ProfileApi } from "../api";
import { useAuth } from "../auth";
import { Avatar } from "../components/Avatar";
import { Button, ButtonLink } from "../components/ui/Button";
import { Chip } from "../components/ui/Chip";
import { Form } from "../components/ui/Form";
import { AdminIcon, MailIcon } from "../components/icons";
import { ImageUpload } from "../components/ImageUpload";
import { AccountRequestForm } from "../components/AccountRequestForm";
import { CalendarFeedCard } from "../components/CalendarFeedCard";
import { NotificationSettingsCard } from "../components/NotificationSettingsCard";
import { ProfileEmailForm, ProfilePasswordForm } from "../components/ProfileSecurityForms";
import { ZonePicker } from "../components/ui/ZonePicker";
import { errorText } from "../lib/errors";
import { useUnsavedChanges } from "../lib/unsavedChanges";
import { Checkbox, Field, FormError, Input } from "../components/ui/Field";
import { Skeleton } from "../components/ui/Skeleton";
import { HouseholdSkeleton } from "../components/HouseholdSkeleton";

const HouseholdCard = lazy(() => import("../components/HouseholdCard").then((m) => ({ default: m.HouseholdCard })));
const GuardianConsentCard = lazy(() => import("../components/GuardianConsentCard").then((m) => ({ default: m.GuardianConsentCard })));

type Section = { id: string; label: string; description: string; adultOnly?: boolean };

// Me groups its rows under quiet headings, each with a one-line description (DEC-99).
const sectionGroups: { heading: string; items: Section[] }[] = [
  { heading: "You", items: [
    { id: "details", label: "Profile details", description: "Name, photo and time zone" },
    { id: "security", label: "Email and password", description: "How you sign in" },
    { id: "privacy", label: "Privacy", description: "Hide your contact info from leaders" },
    { id: "notifications", label: "Notifications", description: "Email and notifications on your devices" },
  ] },
  { heading: "Family", items: [
    { id: "household", label: "Household", description: "Family members you sign up with you", adultOnly: true },
    { id: "guardian", label: "Parent or guardian link", description: "Connect a parent or guardian", adultOnly: true },
  ] },
  { heading: "Sharing", items: [
    { id: "calendar", label: "Calendar feed", description: "Add Fold dates to your phone's calendar" },
    { id: "invite", label: "Invite someone", description: "Ask an admin to let someone in", adultOnly: true },
    { id: "requests", label: "Contact and add requests", description: "Requests to reach or add you" },
  ] },
];

export function ProfilePage() {
  const { user, loading, logout, isAdmin, canCompose } = useAuth();
  const { section } = useParams();
  const location = useLocation();
  const [search, setSearch] = useSearchParams();
  if (loading) return <div className="max-w-2xl mx-auto px-4 py-12"><Skeleton kind="page" /></div>;
  if (!user) return <div className="max-w-2xl mx-auto px-4 py-12 text-center">
    <h1 className="text-3xl font-bold mb-6">Your profile</h1>
    <Link to="/login" state={{ from: location.pathname + location.search }} className="text-primary font-semibold">Log in</Link> to view your profile.
  </div>;

  const groups = sectionGroups
    .map((group) => ({ ...group, items: group.items.filter((item) => !item.adultOnly || user.accountType !== "teen") }))
    .filter((group) => group.items.length > 0);
  const selected = groups.flatMap((group) => group.items).find((item) => item.id === section);
  if (section && !selected) return <Navigate to="/profile" replace />;
  const publicName = user.displayName || user.name || "Member";
  // The hub shows headings and descriptions; the desktop sidebar keeps the
  // headings but not the descriptions, so it stays short.
  const menu = (hub: boolean) => <nav aria-label="Profile settings" className="space-y-5">
    {groups.map((group) => {
      const headingId = `profile-group-${hub ? "hub" : "side"}-${group.heading}`;
      return <div key={group.heading} role="group" aria-labelledby={headingId}>
        {hub
          ? <h2 id={headingId} className="mb-2 text-sm font-semibold text-text-muted">{group.heading}</h2>
          : <p id={headingId} className="mb-2 text-sm font-semibold text-text-muted">{group.heading}</p>}
        <div className="rounded-lg border border-border bg-surface divide-y divide-border overflow-hidden">
          {group.items.map((item) => <NavLink key={item.id} to={`/profile/${item.id}`} className={({ isActive }) => `flex min-h-11 items-center justify-between gap-3 px-4 py-2 ${isActive ? "bg-primary-subtle text-primary font-semibold" : "text-text hover:bg-page"}`}>
            <span className="min-w-0">
              {/* The link's name is its whole visible text, label then description (axe label-content-name-mismatch). */}
              <span className="block">{item.label}</span>
              {hub && <>{" "}<span className="block text-sm font-normal text-text-muted">{item.description}</span></>}
            </span>
            <span aria-hidden="true" className="shrink-0">›</span>
          </NavLink>)}
        </div>
      </div>;
    })}
  </nav>;

  if (!selected) return <div className="max-w-xl mx-auto px-4 py-8 space-y-6 wrap-anywhere">
    <h1 className="text-3xl font-bold">Your profile</h1>
    <div className="flex items-center gap-4">
      <Avatar url={user.avatarUrl || undefined} name={publicName} focalX={user.avatarFocalX} focalY={user.avatarFocalY} size="w-16 h-16" textSize="text-2xl" />
      <div><p className="text-xl font-semibold">{publicName}</p><p className="text-text-muted">Choose what you want to update.</p></div>
    </div>
    {canCompose && <Link to="/messages" className={`lg:hidden flex min-h-11 items-center gap-3 rounded-lg border border-border bg-surface px-4 font-medium`}><MailIcon />Messages</Link>}
    {isAdmin && <Link to="/admin" className="sm:hidden flex min-h-11 items-center gap-3 rounded-lg border border-border bg-surface px-4 font-medium"><AdminIcon />Admin</Link>}
    {menu(true)}
    {/* The header already offers Log out on desktop; phones reach it here. */}
    <Button variant="secondary" layout="sm:hidden w-full" onClick={logout}>Log out</Button>
  </div>;

  const securityTab = search.get("tab") === "password" ? "password" : "email";
  function changeSecurityTab(tab: "email" | "password") {
    setSearch((previous) => {
      const next = new URLSearchParams(previous);
      next.set("tab", tab);
      return next;
    }, { replace: true });
  }
  return <div className="max-w-5xl mx-auto px-4 py-8 wrap-anywhere">
    <div className="sm:grid sm:grid-cols-3 sm:gap-8">
      <aside className="hidden sm:block space-y-4">
        <ButtonLink variant="link" to="/profile">Your profile</ButtonLink>
        {menu(false)}
      </aside>
      <div className="min-w-0 sm:col-span-2 space-y-6">
        <ButtonLink variant="link" to="/profile" layout="sm:hidden">Back to your profile</ButtonLink>
        <h1 className="text-3xl font-bold">{selected.label}</h1>
        <div key={selected.id}>
          {section === "details" && <ProfileDetails />}
          {section === "privacy" && <ProfilePrivacy />}
          {section === "notifications" && <NotificationSettingsCard />}
          {section === "security" && <>
            <nav aria-label="Email and password" className="flex flex-wrap gap-2">
              <Chip selected={securityTab === "email"} onClick={() => changeSecurityTab("email")}>Email</Chip>
              <Chip selected={securityTab === "password"} onClick={() => changeSecurityTab("password")}>Password</Chip>
            </nav>
            {securityTab === "email" ? <ProfileEmailForm /> : <ProfilePasswordForm />}
          </>}
          {section === "calendar" && <CalendarFeedCard showHeading={false} />}
          {section === "household" && <Suspense fallback={<HouseholdSkeleton showHeading={false} />}><HouseholdCard showHeading={false} /></Suspense>}
          {section === "guardian" && <Suspense fallback={<Skeleton kind="lines" count={3} label="Loading your guardian links…" />}><GuardianConsentCard /></Suspense>}
          {section === "invite" && <div className="space-y-4">
            <p className="text-text-muted">Ask for an account for a friend, who only hears from us once it’s approved.</p>
            <AccountRequestForm submitLabel="Ask an admin to invite them" subject="friend" onSubmit={(data) => AccountRequests.inviteFriend(data)} />
          </div>}
          {section === "requests" && <div className="space-y-4">
            <p className="text-text-muted">Review your requests and the access you’ve shared.</p>
            <div className="flex flex-col gap-3">
              <ButtonLink variant="secondary" to="/contact-requests">Contact requests and access</ButtonLink>
              <ButtonLink variant="secondary" to="/add-requests">Add request history</ButtonLink>
            </div>
          </div>}
        </div>
      </div>
    </div>
  </div>;
}

function ProfileDetails() {
  const { user, refresh } = useAuth();
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
  const dn = displayName ?? user?.displayName ?? "";
  const fullName = name ?? user?.name ?? "";
  const ph = phone ?? user?.phone ?? "";
  const tz = zone ?? user?.timezone ?? "";
  // Leaving with edits that aren't saved yet asks first (DEC-98).
  const { guard } = useUnsavedChanges(!!user && (dn.trim() !== user.displayName || fullName.trim() !== user.name || ph.trim() !== user.phone || tz.trim() !== user.timezone));
  if (!user) return null;
  const focalX = user.avatarFocalX;
  const focalY = user.avatarFocalY;

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setSaving(true); setError(null); setSaved(false);
    try {
      await ProfileApi.update({ displayName: dn, name: fullName, phone: ph, ...(tz ? { timezone: tz } : {}) });
      await refresh();
      setSaved(true);
    } catch (err) { setError(errorText(err, "We couldn't save your changes.")); }
    finally { setSaving(false); }
  }
  async function savePictureFile(file: File | null) {
    if (!file) return;
    setPictureBusy(true); setPictureError(null); setPictureStatus("Saving picture…");
    try {
      await ProfileApi.uploadAvatar(file); await refresh(); setPictureStatus("Picture saved.");
    } catch (err) { setPictureStatus(null); setPictureError(errorText(err, "We couldn't save your picture.")); }
    finally { setPictureBusy(false); }
  }
  async function saveFocal(next: { x: number; y: number }) {
    if (next.x === focalX && next.y === focalY) return;
    setPictureBusy(true); setPictureError(null); setPictureStatus("Saving picture…");
    try {
      await ProfileApi.update({ avatarFocal: next }); await refresh(); setPictureStatus("Picture saved.");
    } catch (err) { setPictureStatus(null); setPictureError(errorText(err, "We couldn't save your picture.")); }
    finally { setPictureBusy(false); }
  }
  async function removeAvatar() {
    if (!confirm("Remove your profile picture?")) return;
    setPictureBusy(true); setPictureError(null); setPictureStatus(null);
    try {
      await ProfileApi.deleteAvatar(); await refresh(); setPictureStatus("Picture removed.");
    } catch (err) { setPictureError(errorText(err, "We couldn't remove that.")); }
    finally { setPictureBusy(false); }
  }
  return <div className="space-y-6">
    {user.accountType !== "teen" && <div className="bg-surface rounded-lg border border-border p-6">
      <ImageUpload key={user.avatarUrl || "none"} label="Profile picture" initialUrl={user.avatarUrl || undefined} initialFocal={{ x: focalX, y: focalY }} maxDim={512} aspectClass="aspect-square max-w-48!" placeholderName={user.displayName || user.name || "Member"} onFile={savePictureFile} onFocal={saveFocal} />
      <p className="text-xs text-text-muted mt-3">A new picture is saved as soon as you choose it.</p>
      {pictureStatus && <p role="status" className="text-success text-sm mt-3">{pictureStatus}</p>}
      {pictureError && <FormError layout="mt-3">{pictureError}</FormError>}
      {user.avatarUrl && <Button variant="danger-outline" layout="mt-3" onClick={removeAvatar} disabled={pictureBusy}>Remove picture</Button>}
    </div>}
    <Form noValidate onSubmit={save} className="bg-surface rounded-lg border border-border p-6 space-y-4">
      {guard}
      {error && <FormError>{error}</FormError>}
      {saved && <p role="status" className="text-success text-sm">Profile saved.</p>}
      <Field id="profile-full-name" label="Full name"><Input value={fullName} onChange={(e) => setName(e.target.value)} maxLength={100} /></Field>
      <Field id="profile-display-name" label="Preferred name" optional hint="We’ll use this in lists, for example Bob instead of Robert."><Input value={dn} onChange={(e) => setDisplayName(e.target.value)} maxLength={100} /></Field>
      <Field id="profile-phone" label="Phone" optional><Input type="tel" value={ph} onChange={(e) => setPhone(e.target.value)} maxLength={30} /></Field>
      <Field id="profile-tz" label="Time zone for emails" hint="Emails show event times in this zone. On screen, times always follow this device’s clock."><ZonePicker value={tz} onChange={setZone} /></Field>
      <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={saving}>{saving ? "Saving…" : "Save profile"}</Button>
    </Form>
  </div>;
}

// One on/off choice saves the moment it changes (DEC-98). If saving fails the
// box goes back to what it was and says why.
function ProfilePrivacy() {
  const { user, refresh } = useAuth();
  const [value, setValue] = useState<boolean | null>(null);
  const [saved, setSaved] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  if (!user) return null;
  if (user.accountType === "teen") return <p className="text-text-muted">Your email and phone are always hidden from group leaders and event hosts. You, admins, and your confirmed guardians can still see them.</p>;
  const hideContact = value ?? user.hideContactFromLeaders;
  async function change(next: boolean) {
    // The box is disabled while a save is in flight, so changes are one at a
    // time and a failure goes back to the value from before this save.
    setSaving(true); setValue(next); setSaved(false); setError(null);
    try {
      await ProfileApi.update({ hideContactFromLeaders: next }); await refresh();
      setSaved(true);
    } catch (err) {
      setValue(null);
      setError(errorText(err, "We couldn't save your changes."));
    } finally { setSaving(false); }
  }
  return <div className="bg-surface rounded-lg border border-border p-6 space-y-3">
    {error && <FormError>{error}</FormError>}
    <Checkbox label="Hide my contact info from group leaders" description="Hide your email and phone from group leaders and event hosts. Admins can still see them." checked={hideContact} disabled={saving} onChange={(e) => void change(e.target.checked)} />
    <p role="status" className="text-success text-sm">{saved ? "Saved" : ""}</p>
  </div>;
}
