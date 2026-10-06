import { useState } from "react";
import { Link } from "react-router-dom";
import { Profile as ProfileApi } from "../api";
import { useAuth } from "../auth";
import { ImageUpload } from "../components/ImageUpload";

export function ProfilePage() {
  const { user, loading, refresh } = useAuth();
  const [displayName, setDisplayName] = useState<string | null>(null);
  const [name, setName] = useState<string | null>(null);
  const [email, setEmail] = useState<string | null>(null);
  const [phone, setPhone] = useState<string | null>(null);
  const [avatarFile, setAvatarFile] = useState<File | null>(null);
  const [focal, setFocal] = useState<{ x: number; y: number } | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);

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
  const em = email ?? user.email;
  const ph = phone ?? user.phone;
  const focalChanged =
    focal !== null &&
    (focal.x !== user.avatarFocalX || focal.y !== user.avatarFocalY);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setSaving(true);
    setError(null);
    setSaved(false);
    try {
      await ProfileApi.update({ displayName: dn, name: fullName, email: em, phone: ph });
      if (avatarFile) {
        await ProfileApi.uploadAvatar(avatarFile);
      }
      if (focalChanged && focal) {
        await ProfileApi.update({ avatarFocal: focal });
      }
      setAvatarFile(null);
      setFocal(null);
      await refresh();
      setSaved(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Save failed");
    } finally {
      setSaving(false);
    }
  }

  async function removeAvatar() {
    if (!confirm("Remove your profile picture?")) return;
    setSaving(true);
    setError(null);
    try {
      await ProfileApi.deleteAvatar();
      setAvatarFile(null);
      await refresh();
      setSaved(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Remove failed");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="max-w-2xl mx-auto px-4 py-8">
      <h1 className="text-3xl font-extrabold mb-6">Your profile</h1>

      <form onSubmit={save} className="space-y-6">
        {error && <div className="text-red-600 text-sm">{error}</div>}
        {saved && <div className="text-green-700 text-sm">Profile saved.</div>}

        <div className="bg-white rounded-xl border border-gray-200 p-6">
          <ImageUpload
            label="Profile picture"
            initialUrl={user.avatarUrl || undefined}
            initialFocal={{ x: user.avatarFocalX, y: user.avatarFocalY }}
            maxDim={512}
            aspectClass="aspect-square !max-w-[12rem]"
            onFile={setAvatarFile}
            onFocal={setFocal}
          />
          {user.avatarUrl && (
            <button
              type="button"
              onClick={removeAvatar}
              disabled={saving}
              className="btn-secondary !py-1.5 !px-3 text-sm !text-red-600 mt-3"
            >
              Remove picture
            </button>
          )}
        </div>

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
            <label className="label">Email</label>
            <input
              type="email"
              className="input"
              value={em}
              onChange={(e) => setEmail(e.target.value)}
              maxLength={254}
            />
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
          <div className="text-xs text-gray-500">Username: @{user.username} (cannot be changed)</div>
          <button type="submit" disabled={saving} className="btn-primary">
            {saving ? "Saving…" : "Save profile"}
          </button>
        </div>
      </form>
    </div>
  );
}
