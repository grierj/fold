import { Link } from "react-router-dom";
import type { GroupSummary } from "../types";
import { focalPosition } from "../lib/image";
import { formatNextMeeting } from "../lib/when";
import { coverSrc } from "../lib/covers";
import { CalendarIcon } from "./icons";

// Meetup-style group card (DEC-94). One link target for the whole card.
// Phones: a compact row, an 88px cover thumbnail beside the text. sm and up:
// the tall card, cover on top. Either way the text runs name, next meeting,
// "N members · place", then tags as quiet text (the Home hero chips filter).
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
      className="bg-surface rounded-lg shadow-sm hover:shadow-md transition-shadow overflow-hidden border border-border flex gap-3 p-3 sm:p-0 sm:gap-0 sm:flex-col"
    >
      <div className="size-[88px] shrink-0 overflow-hidden rounded-md bg-sunken sm:size-auto sm:h-40 sm:w-full sm:rounded-none">
        <img
          src={coverSrc(group)}
          alt=""
          className="w-full h-full object-cover"
          style={{ objectPosition: focalPosition(group.coverFocalX, group.coverFocalY) }}
          loading="lazy"
        />
      </div>
      <div className="min-w-0 flex-1 sm:px-4 sm:pt-4 sm:pb-4 space-y-0.5">
        {(group.status === "pending" ||
          group.status === "archived" ||
          group.visibility !== "public") && (
          <div className="flex flex-wrap gap-x-2 gap-y-1">
            {group.status === "pending" && (
              <span className="text-xs font-bold uppercase tracking-wide text-warning-on-subtle">
                Pending approval
              </span>
            )}
            {group.status === "archived" && (
              <span className="text-xs font-bold uppercase tracking-wide text-text-muted">Archived</span>
            )}
            {group.visibility === "private" && (
              <span className="text-xs font-bold uppercase tracking-wide text-text-muted">Private</span>
            )}
            {group.visibility === "invite_only" && (
              <span className="inline-block text-xs font-bold uppercase tracking-wide bg-inverse text-inverse-foreground rounded-full px-2 py-0.5">
                Invite only
              </span>
            )}
          </div>
        )}
        <Heading className="font-bold text-base sm:text-lg leading-snug">{group.name}</Heading>
        <div
          className={`text-sm ${next?.soon ? "text-success-on-subtle font-semibold" : next ? "text-primary-hover font-medium" : "text-text-muted"}`}
        >
          {next ? (
            next.soon ? (
              // Today or Tonight: the word carries the meaning, the dot only decorates (DEC-99).
              <span className="inline-flex items-center gap-1.5">
                <span aria-hidden="true" className="size-2 shrink-0 rounded-full bg-success" />
                {next.text}
              </span>
            ) : next.text
          ) : (
            <span className="inline-flex items-center gap-1">
              <CalendarIcon className="h-4 w-4" />
              No upcoming events
            </span>
          )}
        </div>
        <div className="truncate text-sm text-text-muted">{memberLine}</div>
        {role && (
          <div className="text-sm font-medium text-text-muted">
            <span aria-hidden="true">✓</span> {role}
          </div>
        )}
        {tags && <div className="truncate text-sm text-text-muted">{tags}</div>}
      </div>
    </Link>
  );
}
