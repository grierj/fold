import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { GroupCover, Groups } from "../api";
import type { GroupSummary } from "../types";
import { CreateGroupForm, type GroupFormData } from "./GroupDetail";
import { useAuth } from "../auth";
import { ButtonLink } from "../components/ui/Button";
import { EmptyState } from "../components/ui/EmptyState";
import { Illustration } from "../components/ui/Illustration";

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
        <EmptyState
          level={1}
          title="Group created"
          body={<>“{created.name}” is live.</>}
          action={
            <>
              <ButtonLink to={`/groups/${created.id}/events/new`} variant="primary">Schedule the first meeting</ButtonLink>
              <ButtonLink to={`/groups/${created.id}`} variant="secondary">Go to the group</ButtonLink>
            </>
          }
        />
      ) : created ? (
        // Calm, not a warning: nothing is wrong, an admin just has to look (DEC-96).
        <EmptyState
          level={1}
          illustration={<Illustration name="request-sent" />}
          title={`Thanks! An admin will review “${created.name}”.`}
          body="We'll let you know in Alerts when it's approved."
          action={<ButtonLink to="/" variant="primary">Back to Home</ButtonLink>}
        />
      ) : (
        <>
          <h1 className="text-3xl font-bold mb-2">Start a group</h1>
          {!isAdmin && <p className="text-text-muted mb-6">An admin reviews new groups before they go live.</p>}
          <CreateGroupForm onSaved={create} onCancel={() => navigate("/")} />
        </>
      )}
    </div>
  );
}
