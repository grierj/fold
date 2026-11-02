import { Link } from "react-router-dom";

// EventIntro names the group only when the viewer is allowed to see it.
// An opened invite-only event passes hostedBy and must not mention the group.
// A church-wide event reads "Church-wide event" (DEC-99): the server fills
// hostedBy with the church's name for it, and never sends host names to
// anyone but an admin, so the name would only repeat the site.
export function EventIntro({
  hostedBy,
  groupName,
  groupId,
  churchWide,
}: {
  hostedBy?: string;
  groupName?: string;
  groupId?: number;
  churchWide?: boolean;
}) {
  if (churchWide) {
    return <p className="text-sm text-text-muted">Church-wide event</p>;
  }
  if (hostedBy) {
    return <p className="text-sm text-text-muted">Hosted by {hostedBy}</p>;
  }
  if (groupId && groupName) {
    return (
      <p className="text-sm text-text-muted">
        Part of{" "}
        <Link to={`/groups/${groupId}`} className="inline-flex min-h-11 items-center text-primary font-semibold hover:underline">
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
