import { Link } from "react-router-dom";
import type { GroupSummary } from "../types";
import { focalPosition } from "../lib/image";
import { formatNextMeeting } from "../lib/when";
import { coverSrc } from "../lib/covers";
import { CalendarIcon } from "./icons";

// One link target per directory row, with the same image/name/date rhythm
// at every width. Tags stay quiet text; the directory toolbar owns filtering.
// `showRole` is off on My groups, where every card is the viewer's own and
// only "You lead" is worth saying.
export function GroupCard({
  group,
  headingLevel = 3,
  showRole = true,
}: {
  group: GroupSummary;
  headingLevel?: 2 | 3;
  showRole?: boolean;
}) {
  const next = group.nextEventAt ? formatNextMeeting(group.nextEventAt) : null;
  const Heading = headingLevel === 2 ? "h2" : "h3";
  const role =
    group.myRole === "leader" ? "You lead" : group.myRole === "member" && showRole ? "You're in" : null;
  const memberLine = `${group.memberCount} ${group.memberCount === 1 ? "member" : "members"}${
    group.location ? ` · ${group.location}` : ""
  }`;
  const tags = group.tags.slice(0, 3).join(" · ");
  return (
    <Link
      to={`/groups/${group.id}`}
      className="fold-list-row flex gap-4 rounded-sm hover:bg-sunken"
    >
      <div className="size-[88px] shrink-0 overflow-hidden rounded-lg bg-sunken sm:size-28">
        <img
          src={coverSrc(group)}
          alt=""
          className="w-full h-full object-cover"
          style={{ objectPosition: focalPosition(group.coverFocalX, group.coverFocalY) }}
          loading="lazy"
        />
      </div>
      <div className="min-w-0 flex-1 space-y-1">
        {(group.status === "pending" ||
          group.status === "archived" ||
          group.visibility !== "public") && (
          <div className="flex flex-wrap gap-x-2 gap-y-1">
            {group.status === "pending" && (
              <span className="text-sm font-semibold text-warning-on-subtle">
                Pending approval
              </span>
            )}
            {group.status === "archived" && (
              <span className="text-sm font-semibold text-text-muted">Archived</span>
            )}
            {group.visibility === "private" && (
              <span className="text-sm font-semibold text-text-muted">Private</span>
            )}
            {group.visibility === "invite_only" && (
              <span className="inline-block text-sm font-semibold bg-inverse text-inverse-foreground rounded-full px-2 py-0.5">
                Invite only
              </span>
            )}
          </div>
        )}
        <Heading className="font-bold text-lg leading-snug wrap-anywhere">{group.name}</Heading>
        <div
          className={`text-sm ${next?.soon ? "text-success-on-subtle font-semibold" : next ? "text-text font-medium" : "text-text-muted"}`}
        >
          {next ? (
            next.soon ? (
              <span>{next.text}</span>
            ) : next.text
          ) : (
            <span className="inline-flex items-center gap-1">
              <CalendarIcon className="h-4 w-4" />
              No upcoming events
            </span>
          )}
        </div>
        <div className="text-sm text-text-muted wrap-anywhere">{memberLine}</div>
        {role && (
          <div className="text-sm font-medium text-text-muted">
            <span aria-hidden="true">✓</span> {role}
          </div>
        )}
        {tags && <div className="text-sm text-text-muted wrap-anywhere">{tags}</div>}
      </div>
    </Link>
  );
}
