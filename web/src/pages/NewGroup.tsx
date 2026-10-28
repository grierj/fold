import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { GroupCover, Groups } from "../api";
import type { GroupSummary } from "../types";
import { CreateGroupForm, type GroupFormData } from "./GroupDetail";
import { useAuth } from "../auth";

export function NewGroup() {
  const { user, loading, isAdmin } = useAuth();
  const navigate = useNavigate();
  const [created, setCreated] = useState<GroupSummary | null>(null);

  if (!loading && !user) {
    return (
      <div className="max-w-2xl mx-auto px-4 py-12 text-center">
        <p className="text-text-muted">
          You need to{" "}
          <Link to="/login" state={{ from: "/groups/new" }} className="text-primary font-semibold">
            log in
          </Link>{" "}
          to start a group.
        </p>
      </div>
    );
  }

  async function create(data: GroupFormData) {
    const { group } = await Groups.create({
      name: data.name,
      description: data.description,
      tags: data.tags,
      location: data.location,
      visibility: data.visibility,
      memberLimit: data.memberLimit,
    });
    if (data.coverFile) {
      await GroupCover.upload(group.id, data.coverFile);
    }
    if (data.coverFocal) {
      await Groups.update(group.id, { coverFocal: data.coverFocal });
    }
    setCreated(group);
  }

  return (
    <div className="max-w-2xl mx-auto px-4 py-8">
      {created && created.status === "active" ? (
        // Admins' groups (and anyone else's the server activates) are live now.
        <div className="bg-surface rounded-lg border border-border p-8 text-center">
          <h1 className="text-2xl font-bold">Group created</h1>
          <p className="text-text-muted mt-2">
            <span className="font-semibold">“{created.name}”</span> is live.
          </p>
          <Link to={`/groups/${created.id}`} className="btn-primary inline-block mt-6">
            Go to the group
          </Link>
        </div>
      ) : created ? (
        <div className="bg-surface rounded-lg border border-border p-8 text-center">
          <div className="w-14 h-14 mx-auto rounded-full bg-warning-subtle text-warning-on-subtle flex items-center justify-center text-2xl font-bold">
            !
          </div>
          <h1 className="text-2xl font-bold mt-4">Pending approval</h1>
          <p className="text-text-muted mt-2">
            <span className="font-semibold">“{created.name}”</span> was submitted and is waiting
            for a church admin to approve it. You'll be able to manage it as soon as it's
            approved.
          </p>
          <Link to="/" className="btn-primary inline-block mt-6">
            Back to groups
          </Link>
        </div>
      ) : (
        <>
          <h1 className="text-3xl font-bold mb-2">Start a group</h1>
          {!isAdmin && (
            <p className="text-text-muted mb-6">
              New groups are reviewed by a church admin before they appear in the directory.
            </p>
          )}
          <CreateGroupForm onSaved={create} onCancel={() => navigate("/")} />
        </>
      )}
    </div>
  );
}
