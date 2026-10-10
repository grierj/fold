import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { GroupCover, Groups } from "../api";
import type { GroupSummary } from "../types";
import { CreateGroupForm, type GroupFormData } from "./GroupDetail";
import { useAuth } from "../auth";

export function NewGroup() {
  const { user, loading } = useAuth();
  const navigate = useNavigate();
  const [created, setCreated] = useState<GroupSummary | null>(null);

  if (!loading && !user) {
    return (
      <div className="max-w-2xl mx-auto px-4 py-12 text-center">
        <p className="text-gray-600">
          You need to{" "}
          <Link to="/login" state={{ from: "/groups/new" }} className="text-brand font-semibold">
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
      // A picked file is uploaded next and sets the URL itself.
      imageUrl: data.coverFile ? "" : data.imageUrl,
      location: data.location,
      visibility: data.visibility,
    });
    if (data.coverFile) {
      await GroupCover.upload(group.id, data.coverFile);
    }
    if (data.coverFocal) {
      // Omit imageUrl so this update cannot clear the cover just stored.
      await Groups.update(group.id, { coverFocal: data.coverFocal });
    }
    setCreated(group);
  }

  return (
    <div className="max-w-2xl mx-auto px-4 py-8">
      {created ? (
        <div className="bg-white rounded-xl border border-gray-200 p-8 text-center">
          <div className="w-14 h-14 mx-auto rounded-full bg-yellow-100 text-yellow-700 flex items-center justify-center text-2xl font-bold">
            !
          </div>
          <h1 className="text-2xl font-extrabold mt-4">Pending approval</h1>
          <p className="text-gray-600 mt-2">
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
          <h1 className="text-3xl font-extrabold mb-2">Start a new group</h1>
          <p className="text-gray-600 mb-6">
            New groups are reviewed by a church admin before they appear in the directory.
          </p>
          <CreateGroupForm onSaved={create} onCancel={() => navigate("/")} />
        </>
      )}
    </div>
  );
}
