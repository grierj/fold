import { useState } from "react";
import { Link } from "react-router-dom";
import { AccountRequests, Profile as ProfileApi } from "../api";
import { useAuth } from "../auth";
import { ImageUpload } from "../components/ImageUpload";
import { AccountRequestForm } from "../components/AccountRequestForm";
import { ZoneSelect } from "../components/ZoneSelect";

export function ProfilePage() {
  const { user, loading, refresh } = useAuth();
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
  const [newEmail, setNewEmail] = useState("");
  const [emailSaving, setEmailSaving] = useState(false);
  const [emailStatus, setEmailStatus] = useState<string | null>(null);
  const [emailError, setEmailError] = useState<string | null>(null);

  if (loading) {
    return <div className="max-w-2xl mx-auto px-4 py-12 text-gray-500">Loading…</div>;
  }
  if (!user) {
    return (
      <div className="max-w-2xl mx-auto px-4 py-12 text-center">
        <Link to="/login" state={{ from: "/profile" }} className="text-brand font-semibold">
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
      setError(err instanceof Error ? err.message : "Save failed");
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
      setPictureError(err instanceof Error ? err.message : "Could not save picture");
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
      setPictureError(err instanceof Error ? err.message : "Could not save picture");
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
      setEmailError(err instanceof Error ? err.message : "Could not start email change");
    } finally {
      setEmailSaving(false);
    }
  }

  async function changePassword(e: React.FormEvent) {
    e.preventDefault();
    setPasswordError(null);
    setPasswordSaved(false);
    if (newPassword.length < 10) {
      setPasswordError("Password must be at least 10 characters.");
      return;
    }
    if (newPassword !== confirmPassword) {
      setPasswordError("Passwords do not match.");
      return;
    }
    setPasswordSaving(true);
    try {
      await ProfileApi.changePassword(currentPassword, newPassword);
      setCurrentPassword("");
      setNewPassword("");
      setConfirmPassword("");
      setPasswordSaved(true);
    } catch (err) {
      setPasswordError(err instanceof Error ? err.message : "Password change failed");
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
      setPictureError(err instanceof Error ? err.message : "Remove failed");
    } finally {
      setPictureBusy(false);
    }
  }

  return (
    <div className="max-w-2xl mx-auto px-4 py-8">
      <h1 className="text-3xl font-extrabold mb-6">Your profile</h1>

      <div className="bg-white rounded-xl border border-gray-200 p-6 mb-6">
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
        <p className="text-xs text-gray-500 mt-3">
          A new picture is saved as soon as you choose it.
        </p>
        {pictureStatus && <div className="text-green-700 text-sm mt-3">{pictureStatus}</div>}
        {pictureError && <div className="text-red-600 text-sm mt-3">{pictureError}</div>}
        {user.avatarUrl && (
          <button
            type="button"
            onClick={removeAvatar}
            disabled={pictureBusy}
            className="btn-secondary py-1.5! px-3! text-sm text-red-600! mt-3"
          >
            Remove picture
          </button>
        )}
      </div>

      <form onSubmit={save} className="space-y-6">
        {error && <div className="text-red-600 text-sm">{error}</div>}
        {saved && <div className="text-green-700 text-sm">Profile saved.</div>}

        <div className="bg-white rounded-xl border border-gray-200 p-6 space-y-4">
          <div>
            <label className="label">Display name</label>
            <input
              className="input"
              value={dn}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="How your name appears to others"
              maxLength={100}
            />
          </div>
          <div>
            <label className="label">Full name</label>
            <input
              className="input"
              value={fullName}
              onChange={(e) => setName(e.target.value)}
              maxLength={100}
            />
          </div>
          <div>
            <label className="label">Email (you sign in with it)</label>
            <input type="email" className="input bg-gray-50" value={user.email} readOnly />
            <p className="text-xs text-gray-500 mt-1">
              Change it below; the new address gets a verification link first.
            </p>
          </div>
          <div>
            <label className="label">Phone</label>
            <input
              type="tel"
              className="input"
              value={ph}
              onChange={(e) => setPhone(e.target.value)}
              maxLength={30}
            />
          </div>
          <div>
            <label className="label" htmlFor="profile-tz">
              Time zone for emails
            </label>
            <ZoneSelect id="profile-tz" value={tz} onChange={setZone} describedBy="profile-tz-hint" />
            <p id="profile-tz-hint" className="text-xs text-gray-500 mt-1">
              Emails show event times in this zone. On screen, times always follow this device's clock.
            </p>
          </div>
          <button type="submit" disabled={saving} className="btn-primary">
            {saving ? "Saving…" : "Save profile"}
          </button>
        </div>
      </form>

      <form onSubmit={changeEmail} className="bg-white rounded-xl border border-gray-200 p-6 space-y-4 mt-6">
        <h2 className="text-xl font-bold">Change sign-in email</h2>
        {emailError && <div className="text-red-600 text-sm">{emailError}</div>}
        {emailStatus && <div className="text-green-700 text-sm">{emailStatus}</div>}
        <div>
          <label className="label" htmlFor="new-email">
            New email
          </label>
          <input
            id="new-email"
            type="email"
            className="input"
            value={newEmail}
            onChange={(e) => setNewEmail(e.target.value)}
            autoComplete="email"
            required
          />
          <p className="text-xs text-gray-500 mt-1">
            A verification link goes to the new address, and a notice to {user.email}. The
            change applies after you verify, and your other sessions are signed out.
          </p>
        </div>
        <button type="submit" disabled={emailSaving} className="btn-primary">
          {emailSaving ? "Sending…" : "Send verification link"}
        </button>
      </form>

      <form onSubmit={changePassword} className="bg-white rounded-xl border border-gray-200 p-6 space-y-4 mt-6">
        <h2 className="text-xl font-bold">Change password</h2>
        {passwordError && <div className="text-red-600 text-sm">{passwordError}</div>}
        {passwordSaved && <div className="text-green-700 text-sm">Password changed.</div>}
        <div>
          <label className="label" htmlFor="current-password">Current password</label>
          <input
            id="current-password"
            type="password"
            className="input"
            value={currentPassword}
            onChange={(e) => setCurrentPassword(e.target.value)}
            autoComplete="current-password"
            required
          />
        </div>
        <div>
          <label className="label" htmlFor="new-password">New password</label>
          <input
            id="new-password"
            type="password"
            className="input"
            value={newPassword}
            onChange={(e) => setNewPassword(e.target.value)}
            autoComplete="new-password"
            required
          />
          <p className="text-xs text-gray-500 mt-1">Must be at least 10 characters.</p>
        </div>
        <div>
          <label className="label" htmlFor="confirm-password">Confirm new password</label>
          <input
            id="confirm-password"
            type="password"
            className="input"
            value={confirmPassword}
            onChange={(e) => setConfirmPassword(e.target.value)}
            autoComplete="new-password"
            required
          />
        </div>
        <button type="submit" disabled={passwordSaving} className="btn-primary">
          {passwordSaving ? "Saving…" : "Change password"}
        </button>
      </form>

      {user.accountType !== "teen" && (
        <div className="mt-6">
          <h2 className="text-xl font-bold mb-3">Invite a friend</h2>
          <p className="text-sm text-gray-600 mb-3">
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
    </div>
  );
}
