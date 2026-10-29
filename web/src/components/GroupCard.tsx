import { Link } from "react-router-dom";
import type { GroupSummary } from "../types";
import { focalPosition } from "../lib/image";
import { formatWhen } from "../lib/when";
import { coverSrc } from "../lib/covers";
import { CalendarIcon } from "./icons";
import { Chip } from "./ui/Chip";

// Meetup-style group card: cover image, name, member count, next event, location.
// Tags sit outside the link so they can toggle the directory filter.
// Tags match case-insensitively (as the directory filter does).
export function GroupCard({
  group,
  onTagClick,
  selectedTags = [],
  headingLevel = 3,
}: {
  group: GroupSummary;
  onTagClick?: (tag: string) => void;
  selectedTags?: string[];
  headingLevel?: 2 | 3;
}) {
  const Heading = headingLevel === 2 ? "h2" : "h3";
  const selectedLower = selectedTags.map((t) => t.toLowerCase());
  const isSelected = (t: string) => selectedLower.includes(t.toLowerCase());
  return (
    <div className="bg-surface rounded-lg shadow-sm hover:shadow-md transition-shadow overflow-hidden border border-border flex flex-col">
      <Link to={`/groups/${group.id}`} className="block">
        <div className="h-40 w-full overflow-hidden bg-sunken">
          <img
            src={coverSrc(group)}
            alt=""
            className="w-full h-full object-cover"
            style={{ objectPosition: focalPosition(group.coverFocalX, group.coverFocalY) }}
            loading="lazy"
          />
        </div>
        <div className="px-4 pt-4">
          <Heading className="font-bold text-lg leading-snug">{group.name}</Heading>
          {(group.status === "pending" ||
            group.status === "archived" ||
            group.visibility !== "public") && (
            <div className="flex flex-wrap gap-1 mt-1">
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
        </div>
      </Link>
      {group.tags.length > 0 && (
        <div className="px-4 pt-2 flex flex-wrap gap-1">
          {group.tags.slice(0, 3).map((t) =>
            onTagClick ? (
              <Chip key={t} size="sm" selected={isSelected(t)} onClick={() => onTagClick(t)}>
                {t}
              </Chip>
            ) : (
              <span
                key={t}
                className="text-xs font-medium bg-sunken text-text-muted rounded-full px-2 py-0.5"
              >
                {t}
              </span>
            )
          )}
        </div>
      )}
      <Link to={`/groups/${group.id}`} className="block px-4 pb-4 pt-2 mt-auto">
        <div className="text-sm text-text-muted space-y-0.5">
          <div>
            <span className="font-semibold text-text">{group.memberCount}</span>{" "}
            {group.memberCount === 1 ? "member" : "members"}
            {group.leaderName ? ` · Organized by ${group.leaderName}` : ""}
          </div>
          <div className={group.nextEventAt ? "text-primary-hover font-medium" : "text-text-muted"}>
            {group.nextEventAt ? (
              formatWhen(group.nextEventAt)
            ) : (
              <span className="inline-flex items-center gap-1">
                <CalendarIcon className="h-4 w-4" />
                No upcoming events
              </span>
            )}
          </div>
          {group.location && <div className="truncate">{group.location}</div>}
        </div>
      </Link>
    </div>
  );
}
