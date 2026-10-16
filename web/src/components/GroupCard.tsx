import { Link } from "react-router-dom";
import type { GroupSummary } from "../types";
import { focalPosition } from "../lib/image";
import { formatWhen } from "../lib/when";

// The cover shown when a group has none: the old rose and purple fade, an
// illustration color now (DEC-60). An <img> source cannot read CSS variables,
// so these stay literal until cover patterns replace them (UI makeover PR 8).
const COVER_FROM = "#f64060"; // color-ok: placeholder cover image
const COVER_TO = "#7c3aed"; // color-ok: placeholder cover image

const PLACEHOLDER =
  "data:image/svg+xml," +
  encodeURIComponent(
    `<svg xmlns="http://www.w3.org/2000/svg" width="640" height="360"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="${COVER_FROM}"/><stop offset="1" stop-color="${COVER_TO}"/></linearGradient></defs><rect width="640" height="360" fill="url(#g)"/></svg>`
  );

export function coverImage(url: string | undefined | null): string {
  return url && url.trim() ? url : PLACEHOLDER;
}

// Meetup-style group card: cover image, name, member count, next event, location.
// Tags sit outside the link so they can toggle the directory filter.
// Tags match case-insensitively (as the directory filter does).
export function GroupCard({
  group,
  onTagClick,
  selectedTags = [],
}: {
  group: GroupSummary;
  onTagClick?: (tag: string) => void;
  selectedTags?: string[];
}) {
  const selectedLower = selectedTags.map((t) => t.toLowerCase());
  const isSelected = (t: string) => selectedLower.includes(t.toLowerCase());
  return (
    <div className="bg-surface rounded-lg shadow-sm hover:shadow-md transition-shadow overflow-hidden border border-border flex flex-col">
      <Link to={`/groups/${group.id}`} className="block">
        <div className="h-40 w-full overflow-hidden bg-sunken">
          <img
            src={coverImage(group.imageUrl)}
            alt=""
            className="w-full h-full object-cover"
            style={{ objectPosition: focalPosition(group.coverFocalX, group.coverFocalY) }}
            loading="lazy"
          />
        </div>
        <div className="px-4 pt-4">
          <h3 className="font-bold text-lg leading-snug">{group.name}</h3>
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
              <button
                key={t}
                type="button"
                onClick={() => onTagClick(t)}
                aria-pressed={isSelected(t)}
                className={
                  isSelected(t)
                    ? "inline-flex min-h-6 items-center gap-0.5 text-xs font-semibold bg-primary text-primary-foreground rounded-full px-2 py-0.5"
                    : "inline-flex min-h-6 items-center text-xs font-medium bg-sunken text-text-muted hover:bg-border rounded-full px-2 py-0.5"
                }
              >
                {isSelected(t) && <span aria-hidden="true">✓</span>}
                {t}
              </button>
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
            {group.nextEventAt ? formatWhen(group.nextEventAt) : "No events scheduled yet"}
          </div>
          {group.location && <div className="truncate">{group.location}</div>}
        </div>
      </Link>
    </div>
  );
}
