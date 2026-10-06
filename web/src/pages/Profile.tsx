import { useRef, useState } from "react";
import { Link } from "react-router-dom";
import { Profile as ProfileApi } from "../api";
import { useAuth } from "../auth";

export function ProfilePage() {
  const { user, loading, refresh } = useAuth();
  const [displayName, setDisplayName] = useState<string | null>(null);
  const [name, setName] = useState<string | null>(null);
  const [email, setEmail] = useState<string | null>(null);
  const [phone, setPhone] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [avatarBusy, setAvatarBusy] = useState(false);
  const [avatarError, setAvatarError] = useState<string | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);

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

  // Uncontrolled-initialized state: seed from the user on first render.
  const dn = displayName ?? user.displayName;
  const fullName = name ?? user.name;
  const em = email ?? user.email;
  const ph = phone ?? user.phone;

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setSaving(true);
    setError(null);
    setSaved(false);
    try {
      await ProfileApi.update({ displayName: dn, name: fullName, email: em, phone: ph });
      await refresh();
      setSaved(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Save failed");
    } finally {
      setSaving(false);
    }
  }

  async function uploadAvatar(file: File) {
    setAvatarBusy(true);
    setAvatarError(null);
    try {
      await ProfileApi.uploadAvatar(file);
      await refresh();
    } catch (err) {
      setAvatarError(err instanceof Error ? err.message : "Upload failed");
    } finally {
      setAvatarBusy(false);
    }
  }

  async function removeAvatar() {
    if (!confirm("Remove your profile picture?")) return;
    setAvatarBusy(true);
    setAvatarError(null);
    try {
      await ProfileApi.deleteAvatar();
      await refresh();
    } catch (err) {
      setAvatarError(err instanceof Error ? err.message : "Remove failed");
    } finally {
      setAvatarBusy(false);
    }
  }

  return (
    <div className="max-w-2xl mx-auto px-4 py-8">
      <h1 className="text-3xl font-extrabold mb-6">Your profile</h1>

      <div className="bg-white rounded-xl border border-gray-200 p-6 mb-6 flex items-center gap-5">
        <div className="w-20 h-20 rounded-full overflow-hidden bg-brand-light text-brand-dark flex items-center justify-center text-3xl font-bold shrink-0">
          {user.avatarUrl ? (
            <img src={user.avatarUrl} alt="" className="w-full h-full object-cover" />
          ) : (
            (dn || user.username).charAt(0).toUpperCase()
          )}
        </div>
        <div>
          <div className="font-bold text-lg">{dn || user.username}</div>
          <div className="text-sm text-gray-500">@{user.username}</div>
          <div className="flex gap-2 mt-2">
            <button
              onClick={() => fileRef.current?.click()}
              disabled={avatarBusy}
              className="btn-secondary !py-1.5 !px-3 text-sm"
            >
              {avatarBusy ? "…" : user.avatarUrl ? "Change picture" : "Add picture"}
            </button>
            {user.avatarUrl && (
              <button
                onClick={removeAvatar}
                disabled={avatarBusy}
                className="btn-secondary !py-1.5 !px-3 text-sm !text-red-600"
              >
                Remove
              </button>
            )}
            <input
              ref={fileRef}
              type="file"
              accept="image/jpeg,image/png,image/gif,image/webp"
              className="hidden"
              onChange={(e) => {
                const f = e.target.files?.[0];
                if (f) uploadAvatar(f);
                e.target.value = "";
              }}
            />
          </div>
          {avatarError && <div className="text-sm text-red-600 mt-2">{avatarError}</div>}
          <p className="text-xs text-gray-500 mt-2">JPEG, PNG, GIF or WebP, up to 5MB.</p>
        </div>
      </div>

      <form onSubmit={save} className="bg-white rounded-xl border border-gray-200 p-6 space-y-4">
        {error && <div className="text-red-600 text-sm">{error}</div>}
        {saved && <div className="text-green-700 text-sm">Profile saved.</div>}
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
        <button type="submit" disabled={saving} className="btn-primary">
          {saving ? "Saving…" : "Save profile"}
        </button>
      </form>
    </div>
  );
}
