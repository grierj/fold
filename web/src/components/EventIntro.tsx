import { Link } from "react-router-dom";

// EventIntro names the group only when the viewer is allowed to see it.
// An opened invite-only event passes hostedBy and must not mention the group.
export function EventIntro({
  hostedBy,
  groupName,
  groupId,
}: {
  hostedBy?: string;
  groupName?: string;
  groupId?: number;
}) {
  if (hostedBy) {
    return <p className="text-text-muted">Hosted by {hostedBy}</p>;
  }
  if (groupId && groupName) {
    return (
      <p className="text-text-muted">
        Part of{" "}
        <Link to={`/groups/${groupId}`} className="text-primary font-semibold">
          {groupName}
        </Link>
      </p>
    );
  }
  return null;
}

export function openedEventWarning(groupName: string): string {
  return `Only leaders will see who signed up, so members of ${groupName} aren't revealed.`;
}
